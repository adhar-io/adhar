package db

import (
	"context"
	"fmt"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/platform/logger"

	"github.com/spf13/cobra"
)

var createCmd = &cobra.Command{
	Use:   "create",
	Short: "Create new database",
	Long: `Create a new managed database (Crossplane CompositeDatabase).

The database engine is taken from --type (or --engine); --version and --size
configure the engine version and storage size.

Examples:
  adhar db create --name=myapp --type=postgresql --version=15
  adhar db create --name=myapp --engine=mysql --version=8.0 --size=50Gi`,
	RunE: runCreate,
}

var (
	dbEngine        string
	dbVersion       string
	dbSize          string
	dbInstanceClass string
)

func init() {
	createCmd.Flags().StringVar(&dbEngine, "engine", "", "Database engine (postgresql, mysql, mariadb, mongodb, redis)")
	createCmd.Flags().StringVar(&dbVersion, "version", "", "Database engine version")
	createCmd.Flags().StringVar(&dbSize, "size", "", "Storage size (e.g. 20Gi, 100Gi)")
	createCmd.Flags().StringVar(&dbInstanceClass, "instance-class", "", "Instance size/type for the database")
}

func runCreate(cmd *cobra.Command, args []string) error {
	if dbName == "" {
		return fmt.Errorf("--name is required for database creation")
	}

	engine := dbEngine
	if engine == "" {
		engine = dbType
	}
	if engine == "" {
		return fmt.Errorf("database engine is required (use --engine or --type)")
	}
	if !validEngine(engine) {
		return fmt.Errorf("invalid engine %q (allowed: postgresql, mysql, mariadb, mongodb, redis)", engine)
	}
	if dbVersion == "" {
		return fmt.Errorf("--version is required for database creation")
	}

	ns := dbNamespace()
	logger.Info(fmt.Sprintf("🗄️ Creating database: %s (engine: %s, provider: %s)", dbName, engine, helpers.ActiveProvider()))

	parameters := map[string]interface{}{
		"engine":        engine,
		"engineVersion": dbVersion,
	}
	if dbSize != "" {
		parameters["storageSize"] = dbSize
	}
	if dbInstanceClass != "" {
		parameters["instanceClass"] = dbInstanceClass
	}

	// Provider-aware, engine-discriminated selection — the same contract the
	// Console uses, so `adhar db create` resolves to exactly one composition
	// (e.g. local CNPG for postgresql, or AWS RDS on a cloud platform).
	obj := helpers.NewXR("CompositeDatabase", dbName, ns, "database",
		map[string]string{"engine": engine},
		map[string]interface{}{"parameters": parameters})

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	if err := helpers.ApplyXR(ctx, "compositedatabases", obj); err != nil {
		return fmt.Errorf("create database: %w", err)
	}

	fmt.Println(helpers.CreateSuccess(fmt.Sprintf("Database %s created in namespace %s", dbName, ns)))
	return nil
}

func validEngine(engine string) bool {
	switch engine {
	case "postgresql", "mysql", "mariadb", "mongodb", "redis":
		return true
	}
	return false
}
