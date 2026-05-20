package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestDoctor_RequiresInit(t *testing.T) {
	withCwd(t, t.TempDir()) // no .leonard/
	rt := &fakeRuntime{}
	_, err := runRoot(t, rt, "doctor")
	if err == nil || !strings.Contains(err.Error(), "leonard init") {
		t.Fatalf("expected init-required error, got %v", err)
	}
	if len(rt.doctorCalls) != 0 {
		t.Errorf("Doctor should not be invoked without .leonard/")
	}
}

func TestDoctor_PrintsHealthReport(t *testing.T) {
	mkDataDir(t)
	rt := &fakeRuntime{
		doctorOut: DoctorReport{
			StorePath:     "/tmp/proj/.leonard/leonard.db",
			LastIndexedAt: time.Now().Add(-15 * time.Minute).Unix(),
			TotalFiles:    108,
			TotalSymbols:  2417,
			FilesByLanguage: []LanguageCount{
				{"go", 72}, {"python", 3}, {"typescript", 33},
			},
			SymbolsByLanguage: []LanguageCount{
				{"go", 1842}, {"python", 40}, {"typescript", 535},
			},
			EmptyFiles:         []string{"src/api.py", "src/auth.py"},
			StaleFiles:         []string{"deleted/old.go"},
			DecisionCount:      2,
			StaleDecisionCount: 1,
			UnverifiedClaims:   3,
		},
	}
	out, err := runRoot(t, rt, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\nout=%s", err, out)
	}

	for _, want := range []string{
		"project health",
		"/tmp/proj/.leonard/leonard.db",
		"files:    108 total",
		"go           72",
		"python       3",
		"typescript   33",
		"symbols:  2417 total",
		"parse-failure suspects: 2",
		"src/api.py",
		"src/auth.py",
		"stale files: 1",
		"deleted/old.go",
		"decisions: 2 total",
		"1 stale",
		"claims:    3 unverified",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestDoctor_EmptyStoreHasFriendlyText(t *testing.T) {
	mkDataDir(t)
	rt := &fakeRuntime{} // zero-value DoctorReport
	out, err := runRoot(t, rt, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if !strings.Contains(out, "never — run `leonard index`") {
		t.Errorf("expected never-indexed hint, got:\n%s", out)
	}
	if strings.Contains(out, "parse-failure suspects") || strings.Contains(out, "stale files") {
		t.Errorf("empty report shouldn't show Issues section:\n%s", out)
	}
}

func TestDoctor_TruncatesLongEmptyFileList(t *testing.T) {
	mkDataDir(t)
	many := make([]string, 0, 25)
	for i := 0; i < 25; i++ {
		many = append(many, "broken/file_xx.py")
	}
	rt := &fakeRuntime{doctorOut: DoctorReport{
		StorePath:  "/tmp/proj/.leonard/leonard.db",
		EmptyFiles: many,
	}}
	out, err := runRoot(t, rt, "doctor")
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if !strings.Contains(out, "and 15 more") {
		t.Errorf("expected truncation indicator (25 - 10 sample = 15 more): %s", out)
	}
}

func TestRenderDoctorReport_HumanDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{5 * time.Minute, "5m"},
		{3 * time.Hour, "3h"},
		{2 * 24 * time.Hour, "2d"},
	}
	for _, c := range cases {
		got := humanDuration(c.d)
		if got != c.want {
			t.Errorf("humanDuration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestRenderDoctorReport_ZeroStateNoZeroDuration(t *testing.T) {
	var out bytes.Buffer
	renderDoctorReport(&out, DoctorReport{StorePath: "/x"}, time.Now())
	if strings.Contains(out.String(), "ago") {
		t.Errorf("never-indexed shouldn't print an `ago` line: %s", out.String())
	}
}
