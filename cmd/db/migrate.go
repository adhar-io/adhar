package db

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/k8s"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Run database schema migrations",
	Long: `Run schema migrations against a managed database by launching a Kubernetes
Job that executes a migration tool image. The Job is injected with the database
connection string from the composed CNPG connection secret (<name>-app, key
"uri" by default), so it targets the real managed database.

The default image is golang-migrate (migrate/migrate). Point --image at your own
migration tool and pass --args to control its invocation; $(DATABASE_URL) is
substituted with the connection string at runtime.

Note: golang-migrate expects a "postgres://" URL and a migrations source; the
CNPG secret's "uri" key uses the "postgresql://" scheme. Supply --args and a
source (baked into your image or mounted) for your tool as needed.

Examples:
  adhar db migrate --name=myapp --action=up
  adhar db migrate --name=myapp --action=down
  adhar db migrate --name=myapp --action=status
  adhar db migrate --name=myapp --action=up --image=myorg/migrations:1.2 \
      --args=-path,/migrations,-database,$(DATABASE_URL),up`,
	RunE: runMigrate,
}

var (
	migrateAction    string
	migratePath      string
	migrateImage     string
	migrateArgs      []string
	migrateSecret    string
	migrateSecretKey string
)

func init() {
	migrateCmd.Flags().StringVarP(&migrateAction, "action", "a", "status", "Migration action (up, down, status)")
	migrateCmd.Flags().StringVar(&migratePath, "path", "/migrations", "Migrations source path inside the image")
	migrateCmd.Flags().StringVar(&migrateImage, "image", "migrate/migrate:4", "Migration tool image")
	migrateCmd.Flags().StringSliceVar(&migrateArgs, "args", nil, "Override the migration tool arguments (comma-separated)")
	migrateCmd.Flags().StringVar(&migrateSecret, "secret", "", "Connection secret name (default <name>-app)")
	migrateCmd.Flags().StringVar(&migrateSecretKey, "secret-key", "uri", "Key in the connection secret holding the DB URL")
}

func runMigrate(cmd *cobra.Command, args []string) error {
	if dbName == "" {
		return fmt.Errorf("--name is required for migration operations")
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	clientset, err := k8s.GetClientset()
	if err != nil {
		return fmt.Errorf("could not connect to the cluster (is it running? try `adhar up`): %w", err)
	}

	switch migrateAction {
	case "up", "down":
		return runMigrationJob(ctx, clientset, migrateAction)
	case "status":
		return migrationStatus(ctx, clientset)
	default:
		return fmt.Errorf("invalid --action %q (allowed: up, down, status)", migrateAction)
	}
}

// runMigrationJob launches a batch/v1 Job running the migration tool image with
// the database URL wired in from the composed connection secret.
func runMigrationJob(ctx context.Context, clientset *kubernetes.Clientset, action string) error {
	ns := dbNamespace()
	secretName := migrateSecret
	if secretName == "" {
		secretName = dbName + "-app"
	}

	jobArgs := migrateArgs
	if len(jobArgs) == 0 {
		jobArgs = []string{"-path", migratePath, "-database", "$(DATABASE_URL)", action}
	}

	jobName := fmt.Sprintf("%s-migrate-%s-%d", dbName, action, time.Now().Unix())
	logger.Info(fmt.Sprintf("🔄 Running %s migration for database %s (job: %s)", action, dbName, jobName))

	backoff := int32(2)
	ttl := int32(600)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: ns,
			Labels: map[string]string{
				"adhar.io/managed-by": "adhar-cli",
				"adhar.io/migration":  dbName,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:  "migrate",
						Image: migrateImage,
						Args:  jobArgs,
						Env: []corev1.EnvVar{{
							Name: "DATABASE_URL",
							ValueFrom: &corev1.EnvVarSource{
								SecretKeyRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
									Key:                  migrateSecretKey,
								},
							},
						}},
					}},
				},
			},
		},
	}

	if _, err := clientset.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create migration job: %w", err)
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("Migration job %s started in namespace %s", jobName, ns)))
	fmt.Println(helpers.CreateMuted(fmt.Sprintf("   Logs: kubectl -n %s logs job/%s -f", ns, jobName)))
	return nil
}

// migrationStatus lists the migration Jobs run for this database and their state.
func migrationStatus(ctx context.Context, clientset *kubernetes.Clientset) error {
	ns := dbNamespace()
	logger.Info(fmt.Sprintf("📊 Migration history for database: %s", dbName))

	jobs, err := clientset.BatchV1().Jobs(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "adhar.io/migration=" + dbName,
	})
	if err != nil {
		return fmt.Errorf("list migration jobs: %w", err)
	}
	if len(jobs.Items) == 0 {
		fmt.Println(helpers.InfoStyle.Render("No migration jobs found for this database."))
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "JOB\tSTATE\tSUCCEEDED\tFAILED\tAGE")
	for i := range jobs.Items {
		j := &jobs.Items[i]
		state := "Running"
		switch {
		case j.Status.Succeeded > 0:
			state = "Succeeded"
		case j.Status.Failed > 0:
			state = "Failed"
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\n",
			j.Name, state, j.Status.Succeeded, j.Status.Failed,
			formatDBAge(j.CreationTimestamp.Time))
	}
	return w.Flush()
}

func formatDBAge(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
