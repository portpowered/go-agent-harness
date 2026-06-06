package integration

// TestChatWithFakeResponse runs the chat command with mocked inferencer/executor,
// feeds one user line then "exit", and asserts the output contains the banner,
// the fake response, and the goodbye message.
// TODO: fix me.
// func TestChatWithFakeResponse(t *testing.T) {
// 	fakeResponse := "Fake agent response for testing."

// 	inf := &mockInferencer{response: fakeResponse}
// 	exec := &mockToolExecutor{}

// 	agentCLI, err := wire.InitializeMockAgentCLI(exec, inf)
// 	if err != nil {
// 		t.Fatalf("failed to initialize mock CLI: %v", err)
// 	}

// 	testWriter := NewTestWriter()
// 	rootCmd := agentCLI.Generate()
// 	rootCmd.SetIn(strings.NewReader("hello\n exit\n"))
// 	rootCmd.SetOut(testWriter.Stdout())
// 	rootCmd.SetErr(testWriter.Stderr())
// 	rootCmd.SetArgs([]string{"chat"})

// 	ctx := context.Background()
// 	if err := rootCmd.ExecuteContext(ctx); err != nil {
// 		t.Fatalf("failed to execute chat: %v", err)
// 	}

// 	actual := testWriter.StdoutString()
// 	if !strings.Contains(actual, "Port OS Agent Chat") {
// 		t.Errorf("expected banner in output, got: %s", actual)
// 	}
// 	if !strings.Contains(actual, fakeResponse) {
// 		t.Errorf("expected fake response %q in output, got: %s", fakeResponse, actual)
// 	}
// 	if !strings.Contains(actual, "Goodbye!") {
// 		t.Errorf("expected Goodbye! in output, got: %s", actual)
// 	}
// }

// TestChatQuitWithQuitCommand verifies that typing "quit" exits the session.
// func TestChatQuitWithQuitCommand(t *testing.T) {
// 	inf := &mockInferencer{response: "Hi."}
// 	exec := &mockToolExecutor{}

// 	agentCLI, err := wire.InitializeMockAgentCLI(exec, inf)
// 	if err != nil {
// 		t.Fatalf("failed to initialize mock CLI: %v", err)
// 	}

// 	testWriter := NewTestWriter()
// 	rootCmd := agentCLI.Generate()
// 	rootCmd.SetIn(strings.NewReader("quit\n"))
// 	rootCmd.SetOut(testWriter.Stdout())
// 	rootCmd.SetErr(testWriter.Stderr())
// 	rootCmd.SetArgs([]string{"chat"})

// 	ctx := context.Background()
// 	if err := rootCmd.ExecuteContext(ctx); err != nil {
// 		t.Fatalf("failed to execute chat: %v", err)
// 	}

// 	actual := testWriter.StdoutString()
// 	if !strings.Contains(actual, "Goodbye!") {
// 		t.Errorf("expected Goodbye! when quitting, got: %s", actual)
// 	}
// }

// TestChatWithToolCall runs chat with a mock that does one tool call then returns a final message.
// func TestChatWithToolCall(t *testing.T) {
// 	inf := &mockInferencerWithToolCall{
// 		responses: []subsystems.InferenceResult{
// 			{
// 				Message:   messages.Message{Role: messages.RoleAssistant, ToolCalls: []messages.ToolCall{{ID: "tc1", Name: "get_weather", Arguments: `{"city":"Paris"}`}}},
// 				ToolCalls: []messages.ToolCall{{ID: "tc1", Name: "get_weather", Arguments: `{"city":"Paris"}`}},
// 			},
// 			{
// 				Message: messages.NewTextMessage(messages.RoleAssistant, "The weather in Paris is cloudy."),
// 			},
// 		},
// 	}
// 	exec := &mockToolExecutorWithResults{results: map[string]string{"get_weather": "cloudy, 18C"}}

// 	agentCLI, err := wire.InitializeMockAgentCLI(exec, inf)
// 	if err != nil {
// 		t.Fatalf("failed to initialize mock CLI: %v", err)
// 	}

// 	testWriter := NewTestWriter()
// 	rootCmd := agentCLI.Generate()
// 	rootCmd.SetIn(strings.NewReader("What is the weather in Paris?\n exit\n"))
// 	rootCmd.SetOut(testWriter.Stdout())
// 	rootCmd.SetErr(testWriter.Stderr())
// 	rootCmd.SetArgs([]string{"chat"})

// 	ctx := context.Background()
// 	if err := rootCmd.ExecuteContext(ctx); err != nil {
// 		t.Fatalf("failed to execute chat: %v", err)
// 	}

// 	actual := testWriter.StdoutString()
// 	expected := "The weather in Paris is cloudy."
// 	if !strings.Contains(actual, expected) {
// 		t.Errorf("expected %q in output, got: %s", expected, actual)
// 	}
// 	if !strings.Contains(actual, "Goodbye!") {
// 		t.Errorf("expected Goodbye! in output, got: %s", actual)
// 	}
// }
