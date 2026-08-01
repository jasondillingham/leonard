package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
)

// emptyFilesSample is how many zero-symbol paths we print before
// truncating with a count. Same idea as the `leonard index` failure
// sample — a doctor output that scrolls forever is unreadable.
const emptyFilesSample = 10

// newDoctorCmd is `leonard doctor`. Read-only diagnostic: opens the
// store and reports project health (counts, indexed-at age, parse-failure
// suspects, stale files, decision/claim ledger health).
func newDoctorCmd(rt Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Inspect the project's Leonard store and report health.",
		Long:  "Reads .leonard/leonard.db and prints a terse health report: file/symbol counts per language, last-indexed-at age, files with zero extracted symbols (likely parse failures in supported languages), stale paths (file row exists but missing on disk), and decision/claim ledger counts. No writes.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			dataDir := filepath.Join(cwd, dataDirName)
			if _, err := os.Stat(dataDir); err != nil {
				return fmt.Errorf("no %s here — run `leonard init` first", dataDirName)
			}
			rep, err := rt.Doctor(cmd.Context(), cwd, dataDir)
			if err != nil {
				return fmt.Errorf("doctor: %w", err)
			}
			renderDoctorReport(cmd.OutOrStdout(), rep, time.Now())
			// #101: the store can be perfectly healthy while Leonard's
			// guards are switched off by a bad hook matcher. That failure
			// is invisible by construction, so doctor is the right place
			// to look for it.
			renderWiringFindings(cmd.OutOrStdout(), checkHookWiring(cwd))
			return nil
		},
	}
	return cmd
}

// renderDoctorReport formats rep against now. Pulled out for testability
// so we don't depend on time.Now() inside the cobra RunE.
func renderDoctorReport(out interface {
	Write([]byte) (int, error)
}, rep DoctorReport, now time.Time) {
	fmt.Fprintf(out, "leonard: project health\n")
	fmt.Fprintf(out, "  store:        %s\n", rep.StorePath)
	if rep.LastIndexedAt > 0 {
		t := time.Unix(rep.LastIndexedAt, 0)
		fmt.Fprintf(out, "  last indexed: %s  (%s ago)\n", t.Format(time.RFC3339), humanDuration(now.Sub(t)))
	} else {
		fmt.Fprintf(out, "  last indexed: (never — run `leonard index`)\n")
	}

	fmt.Fprintf(out, "\nIndex\n")
	fmt.Fprintf(out, "  files:    %d total\n", rep.TotalFiles)
	for _, lc := range rep.FilesByLanguage {
		fmt.Fprintf(out, "    %-12s %d\n", lc.Language, lc.Count)
	}
	fmt.Fprintf(out, "  symbols:  %d total\n", rep.TotalSymbols)
	for _, lc := range rep.SymbolsByLanguage {
		fmt.Fprintf(out, "    %-12s %d\n", lc.Language, lc.Count)
	}

	if len(rep.EmptyFiles) > 0 || len(rep.StaleFiles) > 0 {
		fmt.Fprintf(out, "\nIssues\n")
	}
	// Bughunt-2 cli F18 (carry-over): a stale file (row exists in
	// store but file is missing on disk) trivially has zero
	// extracted symbols, so it gets double-counted as both
	// "parse-failure suspect" and "stale file." Subtract stale
	// paths from EmptyFiles before reporting parse-failure suspects
	// so each issue surfaces in exactly one category.
	staleSet := map[string]struct{}{}
	for _, p := range rep.StaleFiles {
		staleSet[p] = struct{}{}
	}
	parseSuspects := make([]string, 0, len(rep.EmptyFiles))
	for _, p := range rep.EmptyFiles {
		if _, isStale := staleSet[p]; !isStale {
			parseSuspects = append(parseSuspects, p)
		}
	}
	if len(parseSuspects) > 0 {
		fmt.Fprintf(out, "  parse-failure suspects: %d file(s) with zero extracted symbols\n", len(parseSuspects))
		fmt.Fprintf(out, "    (these likely failed to parse — run `leonard index` for line/message detail)\n")
		shown := parseSuspects
		if len(shown) > emptyFilesSample {
			shown = shown[:emptyFilesSample]
		}
		for _, p := range shown {
			fmt.Fprintf(out, "    %s\n", p)
		}
		if len(parseSuspects) > emptyFilesSample {
			fmt.Fprintf(out, "    … and %d more\n", len(parseSuspects)-emptyFilesSample)
		}
	}
	if len(rep.StaleFiles) > 0 {
		fmt.Fprintf(out, "  stale files: %d (file row in store but missing on disk — run `leonard index`)\n", len(rep.StaleFiles))
		shown := rep.StaleFiles
		if len(shown) > emptyFilesSample {
			shown = shown[:emptyFilesSample]
		}
		for _, p := range shown {
			fmt.Fprintf(out, "    %s\n", p)
		}
		if len(rep.StaleFiles) > emptyFilesSample {
			fmt.Fprintf(out, "    … and %d more\n", len(rep.StaleFiles)-emptyFilesSample)
		}
	}

	fmt.Fprintf(out, "\nLedger\n")
	fmt.Fprintf(out, "  decisions: %d total", rep.DecisionCount)
	if rep.StaleDecisionCount > 0 {
		fmt.Fprintf(out, "  (%d stale — run `leonard decisions stale` for detail)", rep.StaleDecisionCount)
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  claims:    %d unverified", rep.UnverifiedClaims)
	if rep.UnverifiedClaims > 0 {
		fmt.Fprintf(out, "  (run `leonard claims unverified` for detail)")
	}
	fmt.Fprintln(out)
}

// humanDuration is a terse "5m", "3h", "2d" style. Doctor uses it for
// "last indexed N ago" — fine-grained precision isn't useful at that
// granularity, just an at-a-glance "recent vs ancient" signal.
func humanDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
