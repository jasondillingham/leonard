package groundtruth

// VerifyClaimForTest / ListFactsForTest / GetStoryForTest are thin
// wrappers around the unexported MCP handler methods so external
// tests (package groundtruth_test) can invoke them without standing
// up an MCP server. They're declared in the production package
// because Go's unexported-method rules apply at the file boundary
// only when files share a build tag; these are always built.
//
// Each wrapper exists to test ONE handler — there is no surface
// exposed beyond what tests need.

// VerifyClaimForTest exposes verifyClaim. Used by mcp_test.go.
func (a *GroundTruthAdapter) VerifyClaimForTest(in VerifyClaimInput) VerifyClaimOutput {
	return a.verifyClaim(in)
}

// VerifyClaimCheckedForTest exposes verifyClaimChecked so tests can
// exercise the bughunt-11 F6 input-size cap.
func (a *GroundTruthAdapter) VerifyClaimCheckedForTest(in VerifyClaimInput) (VerifyClaimOutput, error) {
	return a.verifyClaimChecked(in)
}

// ListFactsForTest exposes listFacts. Used by mcp_test.go.
func (a *GroundTruthAdapter) ListFactsForTest(in ListFactsInput) ListFactsOutput {
	return a.listFacts(in)
}

// GetStoryForTest exposes getStory. Used by mcp_test.go.
func (a *GroundTruthAdapter) GetStoryForTest(in GetStoryInput) (GetStoryOutput, error) {
	return a.getStory(in)
}
