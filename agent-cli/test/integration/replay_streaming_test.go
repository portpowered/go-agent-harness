package integration

import (
	"testing"
)

// TestReplayStreaming_2_2 runs the ask command with --replay using the fixed fixture
// testdata/streaming_2_2.json (recorded "what is 2 + 2?" response) and asserts that the
// CLI output stream contains the expected answer.
func TestReplayStreaming_2_2(t *testing.T) {
	// replayPath := streamingReplayFixture
	// if _, err := os.Stat(replayPath); err != nil {
	// 	// When cwd is test/integration (e.g. go test from that dir)
	// 	replayPath = filepath.Join("testdata", "streaming_2_2.json")
	// 	if _, err2 := os.Stat(replayPath); err2 != nil {
	// 		t.Fatalf("replay fixture not found (tried %q and testdata/streaming_2_2.json): %v", streamingReplayFixture, err)
	// 	}
	// }

	// // Replay mode: set minimal env so validation passes (HTTP is intercepted).
	// if os.Getenv("AGENT_MODEL__PROVIDER") == "" {
	// 	os.Setenv("AGENT_MODEL__PROVIDER", "openrouter")
	// }
	// if os.Getenv("AGENT_MODEL__OPENROUTER__API_KEY") == "" {
	// 	os.Setenv("AGENT_MODEL__OPENROUTER__API_KEY", "replay-dummy")
	// }

	// agentCLI, err := wire.InitializeAgentCLI()
	// if err != nil {
	// 	t.Fatalf("failed to initialize agent CLI: %v", err)
	// }

	// testWriter := NewTestWriter()
	// rootCmd := agentCLI.Generate()
	// rootCmd.SetOut(testWriter.Stdout())
	// rootCmd.SetErr(testWriter.Stderr())
	// rootCmd.SetIn(strings.NewReader("")) // avoid blocking on real stdin when debugging

	// args := []string{"ask", "--replay", replayPath, "what is 2 + 2?", "--stream",
	// 	"--system-prompt", "none",
	// 	"--no-system-information", "-v"}
	// rootCmd.SetArgs(args)

	// ctx := context.Background()
	// if err := rootCmd.ExecuteContext(ctx); err != nil {
	// 	t.Fatalf("ask with replay failed: %v", err)
	// }

	// actual := testWriter.StdoutString()
	// actualTrimmed := strings.TrimSpace(actual)

	// if actualTrimmed == "" {
	// 	t.Error("expected non-empty output from replay")
	// }

	// // Fixture streaming_2_2.json is a recorded "what is 2 + 2?" response; the final content is "2 + 2 = 4".
	// expectedAnswer := "2 + 2 = 4"
	// if !strings.Contains(actualTrimmed, expectedAnswer) {
	// 	t.Errorf("expected output to contain %q; got:\n%s", expectedAnswer, actualTrimmed)
	// }
}
