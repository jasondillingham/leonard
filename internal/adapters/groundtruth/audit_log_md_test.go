package groundtruth_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasondillingham/leonard/internal/adapters"
)

// auditMdFixture is the same shape as postEditFixture but reads the
// markdown audit log instead of the JSON pending log.
func readAuditLogMD(t *testing.T, projectRoot string) string {
	t.Helper()
	path := filepath.Join(projectRoot, ".leonard", "ground-truth", "audit-log.md")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("read audit-log.md: %v", err)
	}
	return string(data)
}

func TestAuditLogMD_WrittenAlongsidePendingLog(t *testing.T) {
	a, tmp, target := postEditFixture(t, "page.md",
		"Our platform: ExampleSaaS supports HIPAA-compliant workflows.")

	if _, err := a.PostEdit(context.Background(), adapters.PostEditPayload{
		SessionID: "s1",
		Tool:      "Write",
		FilePath:  target,
	}); err != nil {
		t.Fatalf("PostEdit: %v", err)
	}

	md := readAuditLogMD(t, tmp)
	if md == "" {
		t.Fatal("audit-log.md should exist after a finding")
	}
	if !strings.Contains(md, "# Audit log") {
		t.Error("audit-log.md should have a top header")
	}
	if !strings.Contains(md, "## ") {
		t.Errorf("audit-log.md should have at least one section header, got:\n%s", md)
	}
}

func TestAuditLogMD_SectionShape(t *testing.T) {
	a, tmp, target := postEditFixture(t, "page.md",
		"Our marketing: ExampleSaaS supports HIPAA-compliant workflows.")

	if _, err := a.PostEdit(context.Background(), adapters.PostEditPayload{
		SessionID: "session-xyz",
		Tool:      "Write",
		FilePath:  target,
	}); err != nil {
		t.Fatalf("PostEdit: %v", err)
	}

	md := readAuditLogMD(t, tmp)
	for _, want := range []string{
		"page.md",
		"FORBIDDEN HIT",
		"**Claims made:**",
		"FORBIDDEN",
		"do-not-claim.md#",
		"**Tool:** Write",
		"**Session:** session-xyz",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("audit-log.md missing %q in:\n%s", want, md)
		}
	}
}

func TestAuditLogMD_AppendsAcrossCalls(t *testing.T) {
	a, tmp, target := postEditFixture(t, "page.md",
		"We have a mobile app already.")

	for i := 0; i < 3; i++ {
		if _, err := a.PostEdit(context.Background(), adapters.PostEditPayload{
			SessionID: "s1",
			Tool:      "Write",
			FilePath:  target,
		}); err != nil {
			t.Fatalf("PostEdit %d: %v", i, err)
		}
	}

	md := readAuditLogMD(t, tmp)
	if got := strings.Count(md, "## "); got != 3 {
		t.Errorf("section count: want 3, got %d in:\n%s", got, md)
	}
	// Header bootstrap should happen exactly once.
	if got := strings.Count(md, "# Audit log"); got != 1 {
		t.Errorf("top header count: want 1, got %d", got)
	}
}

func TestAuditLogMD_NoEntryWhenNoFindings(t *testing.T) {
	a, tmp, target := postEditFixture(t, "page.md",
		"Today I shipped Go in production. I believe Tuesday releases are better.")

	if _, err := a.PostEdit(context.Background(), adapters.PostEditPayload{
		SessionID: "s1",
		Tool:      "Write",
		FilePath:  target,
	}); err != nil {
		t.Fatalf("PostEdit: %v", err)
	}

	md := readAuditLogMD(t, tmp)
	// Verified + Opinion alone → no section emitted.
	if strings.Contains(md, "## ") {
		t.Errorf("verified-only + opinion should not produce a section, got:\n%s", md)
	}
}

func TestAuditLogMD_FlaggedStatusForUnverified(t *testing.T) {
	// Find unverified findings: "We have 50,000 users" → quantitative
	// pattern fires, no facts match → Unverified.
	a, tmp, target := postEditFixture(t, "page.md",
		"Marketing line: we have 50,000 users on the platform.")

	if _, err := a.PostEdit(context.Background(), adapters.PostEditPayload{
		SessionID: "s1",
		Tool:      "Write",
		FilePath:  target,
	}); err != nil {
		t.Fatalf("PostEdit: %v", err)
	}

	md := readAuditLogMD(t, tmp)
	if md == "" {
		t.Fatal("audit-log.md missing")
	}
	if !strings.Contains(md, "flagged") {
		t.Errorf("status should be 'flagged' for unverified-only, got:\n%s", md)
	}
}

func TestAuditLogMD_EscapesPipes(t *testing.T) {
	// A claim with a literal pipe shouldn't break markdown table
	// rendering elsewhere — defensive escape.
	a, tmp, target := postEditFixture(t, "page.md",
		"Bullet: We have a mobile app | yes | confirmed.")

	if _, err := a.PostEdit(context.Background(), adapters.PostEditPayload{FilePath: target}); err != nil {
		t.Fatalf("PostEdit: %v", err)
	}
	// The matched span shouldn't contain a raw pipe.
	md := readAuditLogMD(t, tmp)
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(line, "- ") {
			// Bullet line — should not have unescaped pipes inside
			// the claim text (between "- " and " — ").
			parts := strings.SplitN(line, " — ", 2)
			claim := strings.TrimPrefix(parts[0], "- ")
			// Either no pipe, or an escaped one.
			if strings.Contains(claim, "|") && !strings.Contains(claim, "\\|") {
				t.Errorf("bullet line has unescaped pipe: %q", line)
			}
		}
	}
}
