/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package cost

// `adhar cost` — what the platform is spending, and on whose behalf.
//
// OpenCost published metrics but nothing read them, so "what does this team
// cost" had no answer short of writing PromQL by hand. This command reads the
// SAME recorded series the budget alerts use
// (observability/adhar-cost-governance), which is the point: showback that
// disagrees with the alert that pages you is worse than no showback.
//
// It reports the cost of resource REQUESTS rather than usage. That is what the
// scheduler reserves and therefore what denies capacity to everyone else — a
// team running at 5% of a large request is costing the platform the full
// reservation, and a usage figure would hide exactly the waste worth reclaiming.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"adhar-io/adhar/cmd/helpers"
	"adhar-io/adhar/globals"

	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
)

const (
	// The recorded series. Named constants because the rule file and this
	// command are one contract; a rename in either place must break the build,
	// not silently produce an empty report.
	seriesHourlyTotal = "adhar:namespace_cost_usd:hourly_total"
	seriesMonthly     = "adhar:namespace_cost_usd:monthly_projected"
	seriesBudget      = "adhar:namespace_budget_usd:monthly"
	seriesByComponent = "adhar:namespace_cost_usd:hourly"
)

var (
	promService string
	showAll     bool
)

// CostCmd is the command group.
var CostCmd = &cobra.Command{
	Use:     "cost",
	Aliases: []string{"spend", "showback"},
	Short:   "What the platform is spending, per namespace and team",
	Long: `▸ **Adhar Cost**

Reads the platform's recorded cost series (published by the
observability/adhar-cost-governance package) and reports spend per namespace,
against any declared budget.

Figures are the cost of resource REQUESTS — what the scheduler reserves, and so
what actually denies capacity to other teams. A workload requesting far more than
it uses shows up here at its full reservation, which is the number worth acting on.

  adhar cost                 spend per namespace, with budget usage
  adhar cost --all           include namespaces with no measurable cost
  adhar cost breakdown       split each namespace into CPU and memory`,
	RunE: runCost,
}

var breakdownCmd = &cobra.Command{
	Use:   "breakdown",
	Short: "Split each namespace's cost into CPU and memory",
	RunE:  runBreakdown,
}

func init() {
	CostCmd.PersistentFlags().StringVar(&promService, "prometheus",
		"prometheus-kube-prometheus-prometheus:9090",
		"Prometheus service as name:port in the platform namespace")
	CostCmd.Flags().BoolVar(&showAll, "all", false, "Include namespaces with no measurable cost")
	CostCmd.AddCommand(breakdownCmd)
}

/* ─────────────── querying ─────────────── */

// sample is one series returned by Prometheus.
type sample struct {
	Labels map[string]string
	Value  float64
}

// query runs an instant query through the API server's service proxy.
//
// The proxy rather than a port-forward or an ingress: it needs no extra network
// path, works identically from a laptop and from inside the cluster, and is
// authorised by the caller's own RBAC — so `adhar cost` can only read what the
// user could already read.
func query(ctx context.Context, cs kubernetes.Interface, expr string) ([]sample, error) {
	raw, err := cs.CoreV1().RESTClient().Get().
		AbsPath("/api/v1/namespaces", globals.AdharSystemNamespace, "services",
			"http:"+promService, "proxy", "api", "v1", "query").
		Param("query", expr).
		DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("querying prometheus via the API server proxy: %w", err)
	}
	var body struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  []interface{}     `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("parsing the prometheus response: %w", err)
	}
	if body.Status != "success" {
		return nil, fmt.Errorf("prometheus refused the query: %s", body.Error)
	}
	out := make([]sample, 0, len(body.Data.Result))
	for _, r := range body.Data.Result {
		if len(r.Value) != 2 {
			continue
		}
		s, _ := r.Value[1].(string)
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			continue
		}
		out = append(out, sample{Labels: r.Metric, Value: f})
	}
	return out, nil
}

func clientset() (kubernetes.Interface, error) {
	cfg, err := helpers.GetKubeConfig()
	if err != nil {
		return nil, fmt.Errorf("no reachable cluster: %w", err)
	}
	return kubernetes.NewForConfig(cfg)
}

/* ─────────────── report ─────────────── */

func runCost(cmd *cobra.Command, args []string) error {
	cs, err := clientset()
	if err != nil {
		return err
	}
	ctx := cmd.Context()

	hourly, err := query(ctx, cs, seriesHourlyTotal)
	if err != nil {
		return err
	}
	if len(hourly) == 0 {
		fmt.Println(helpers.SectionHeading("▸", "Platform cost"))
		fmt.Println("  No cost series yet.")
		fmt.Printf("  %s\n\n", helpers.SubtitleStyle.Render(
			"enable the observability/adhar-cost-governance package (and opencost); the rules take a minute to record"))
		return nil
	}
	monthly := byNamespace(mustQuery(ctx, cs, seriesMonthly))
	budgets := byNamespace(mustQuery(ctx, cs, seriesBudget))

	sort.SliceStable(hourly, func(i, j int) bool { return hourly[i].Value > hourly[j].Value })

	// One pass to decide the SHAPE of the table before drawing it: the totals
	// each column must align to, and whether the budget columns have anything
	// to say at all.
	rows := make([]struct {
		ns          string
		hour, month float64
		budget      float64
		hasBudget   bool
	}, 0, len(hourly))
	var totalHour, totalMonth float64
	anyBudget := false
	for _, s := range hourly {
		ns := s.Labels["namespace"]
		if !showAll && s.Value < 0.0001 {
			continue
		}
		b, hasBudget := budgets[ns]
		anyBudget = anyBudget || hasBudget
		m := monthly[ns]
		totalHour += s.Value
		totalMonth += m
		rows = append(rows, struct {
			ns          string
			hour, month float64
			budget      float64
			hasBudget   bool
		}{ns, s.Value, m, b, hasBudget})
	}

	fmt.Println(helpers.SectionHeading("▸", "Platform cost · by namespace"))

	t := helpers.NewTable(costHeaders(anyBudget)...)

	hourFmt, monthFmt := columnMoney(totalHour), columnMoney(totalMonth)
	for _, r := range rows {
		cells := []string{r.ns, hourFmt(r.hour), monthFmt(r.month), shareBar(r.hour, totalHour)}
		if anyBudget {
			cells = append(cells, budgetCell(r.budget, r.hasBudget), usedCell(r.month, r.budget, r.hasBudget))
		}
		t.Row(cells...)
	}
	fmt.Println(t.Render())

	fmt.Printf("  %s\n", helpers.SubtitleStyle.Render(fmt.Sprintf(
		"total %s/hour · %s/month · %s/year projected",
		money(totalHour), money(totalMonth), money(totalMonth*12))))
	fmt.Printf("  %s\n\n", helpers.SubtitleStyle.Render(
		"figures are the cost of resource REQUESTS, not usage — what the scheduler reserves"))
	if !anyBudget {
		fmt.Printf("  %s\n\n", helpers.SubtitleStyle.Render(
			"no budgets declared — add them to the adhar-cost-governance PrometheusRule to get alerts"))
	}
	return nil
}

func runBreakdown(cmd *cobra.Command, args []string) error {
	cs, err := clientset()
	if err != nil {
		return err
	}
	parts, err := query(cmd.Context(), cs, seriesByComponent)
	if err != nil {
		return err
	}
	if len(parts) == 0 {
		fmt.Println("  No cost series yet — is observability/adhar-cost-governance enabled?")
		return nil
	}
	type split struct{ cpu, ram float64 }
	per := map[string]*split{}
	for _, s := range parts {
		ns := s.Labels["namespace"]
		if per[ns] == nil {
			per[ns] = &split{}
		}
		switch s.Labels["component"] {
		case "cpu":
			per[ns].cpu += s.Value
		case "ram":
			per[ns].ram += s.Value
		}
	}
	names := make([]string, 0, len(per))
	for n := range per {
		names = append(names, n)
	}
	sort.SliceStable(names, func(i, j int) bool {
		a, b := per[names[i]], per[names[j]]
		return a.cpu+a.ram > b.cpu+b.ram
	})

	var grand, cpuTotal, ramTotal float64
	for _, n := range names {
		grand += per[n].cpu + per[n].ram
		cpuTotal += per[n].cpu
		ramTotal += per[n].ram
	}

	fmt.Println(helpers.SectionHeading("▸", "Platform cost · CPU vs memory"))
	t := helpers.NewTable("NAMESPACE", "CPU $/HR", "MEM $/HR", "TOTAL $/HR", "SPLIT")
	// Same column-wide precision as `adhar cost`, for the same reason: these
	// are read down, not one at a time.
	f := columnMoney(grand)
	for _, n := range names {
		s := per[n]
		total := s.cpu + s.ram
		if total < 0.0001 {
			continue
		}
		t.Row(n, f(s.cpu), f(s.ram), f(total), splitBar(s.cpu, total))
	}
	fmt.Println(t.Render())
	if grand > 0 {
		fmt.Printf("  %s\n", helpers.SubtitleStyle.Render(fmt.Sprintf(
			"total %s/hour · %.0f%% cpu, %.0f%% memory",
			money(grand), cpuTotal/grand*100, ramTotal/grand*100)))
	}
	fmt.Printf("  %s\n\n", helpers.SubtitleStyle.Render("a namespace dominated by memory usually wants a smaller request, not a bigger node"))
	return nil
}

// mustQuery returns an empty result rather than failing: a missing budget series
// is the normal state before anyone declares one, and the spend report is useful
// on its own.
func mustQuery(ctx context.Context, cs kubernetes.Interface, expr string) []sample {
	s, err := query(ctx, cs, expr)
	if err != nil {
		return nil
	}
	return s
}

func byNamespace(ss []sample) map[string]float64 {
	m := map[string]float64{}
	for _, s := range ss {
		m[s.Labels["namespace"]] += s.Value
	}
	return m
}

func money(v float64) string {
	if v >= 100 {
		return fmt.Sprintf("$%.0f", v)
	}
	if v >= 1 {
		return fmt.Sprintf("$%.2f", v)
	}
	return fmt.Sprintf("$%.4f", v)
}

func budgetCell(b float64, has bool) string {
	if !has {
		return "—"
	}
	return money(b)
}

// usedCell colours by the same thresholds the alerts fire on, so the table and
// the pager agree about what "over" means.
func usedCell(spend, budget float64, has bool) string {
	if !has || budget <= 0 {
		return "—"
	}
	pct := spend / budget * 100
	label := fmt.Sprintf("%.0f%%", pct)
	switch {
	case pct > 100:
		return helpers.StateDegraded(label)
	case pct > 80:
		return helpers.StatePending(label)
	default:
		return helpers.StateReady(label)
	}
}

// splitBar shows the CPU:memory ratio at a glance — the shape of the spend
// matters more than the absolute number when deciding what to resize.
func splitBar(cpu, total float64) string {
	if total <= 0 {
		return ""
	}
	const width = 12
	n := int(cpu / total * width)
	if n < 0 {
		n = 0
	}
	if n > width {
		n = width
	}
	bar := ""
	for i := 0; i < width; i++ {
		if i < n {
			bar += "▰"
		} else {
			bar += "▱"
		}
	}
	return fmt.Sprintf("%s %.0f%% cpu", bar, cpu/total*100)
}

// columnMoney gives every value in ONE column the same number of decimals,
// chosen from the column's largest value.
//
// `money` picks its precision per value, which is right for a number inside a
// sentence and wrong for a column. A namespace at `$491` printed directly above
// one at `$11.43` made the two look like different units and lined up no digit
// with any other, so the column could not be read down — which is the only
// reason to put numbers in a column.
func columnMoney(max float64) func(float64) string {
	switch {
	case max >= 100:
		return func(v float64) string { return fmt.Sprintf("$%.0f", v) }
	case max >= 1:
		return func(v float64) string { return fmt.Sprintf("$%.2f", v) }
	default:
		return func(v float64) string { return fmt.Sprintf("$%.4f", v) }
	}
}

// shareBar is each namespace's share of total spend.
//
// The fact the table was missing: a report whose first row is 98% of the bill
// should say so, because "which namespace do I look at" is the question the
// command is opened with, and two dollar figures of different magnitudes do not
// answer it at a glance.
func shareBar(v, total float64) string {
	if total <= 0 {
		return ""
	}
	pct := v / total * 100
	const width = 10
	n := int(pct/100*float64(width) + 0.5)
	if n < 0 {
		n = 0
	}
	if n > width {
		n = width
	}
	bar := ""
	for i := 0; i < width; i++ {
		if i < n {
			bar += "▰"
		} else {
			bar += "▱"
		}
	}
	return fmt.Sprintf("%s %4.1f%%", bar, pct)
}

// costHeaders drops BUDGET and USED when nothing declares a budget.
//
// Two columns of "—" is not information. On a platform where no budget has been
// set — the normal state, and the one the command itself prints advice about —
// they were a third of the table's width saying nothing.
func costHeaders(anyBudget bool) []string {
	h := []string{"NAMESPACE", "$/HOUR", "$/MONTH (PROJ.)", "SHARE"}
	if anyBudget {
		h = append(h, "BUDGET", "USED")
	}
	return h
}
