(async () => {
  const probeToolName = "webmcp_o0_probe_tool";
  const missingToolName = "webmcp_o0_missing_tool";
  const safeError = (error) => String(error && error.stack ? error.stack : error);
  const typeOf = (value) => value === null ? "null" : typeof value;
  const read = (getter) => {
    try {
      return { value: getter(), error: "" };
    } catch (error) {
      return { value: undefined, error: safeError(error) };
    }
  };
  const methodReport = (object, name) => {
    let member;
    try {
      member = object == null ? undefined : object[name];
    } catch (error) {
      return { present: false, type: "access-error", length: undefined };
    }
    return {
      present: typeof member === "function",
      type: typeOf(member),
      length: typeof member === "function" ? member.length : undefined
    };
  };
  const objectReport = (readResult, methodNames) => {
    const report = {
      present: readResult.value !== undefined && readResult.value !== null,
      type: typeOf(readResult.value),
      methods: {}
    };
    if (readResult.error) report.accessError = readResult.error;
    for (const name of methodNames) report.methods[name] = methodReport(readResult.value, name);
    return report;
  };
  const descriptorReport = (constructor, name) => {
    try {
      const descriptor = Object.getOwnPropertyDescriptor(constructor.prototype, name);
      return descriptor ? {
        present: true,
        hasGetter: typeof descriptor.get === "function",
        enumerable: Boolean(descriptor.enumerable),
        configurable: Boolean(descriptor.configurable)
      } : { present: false, hasGetter: false, enumerable: false, configurable: false };
    } catch (error) {
      return { present: false, hasGetter: false, enumerable: false, configurable: false };
    }
  };
  const summarizeTools = (value) => {
    if (!Array.isArray(value)) return [];
    return value.map((tool) => ({
      name: tool && tool.name ? String(tool.name) : "",
      title: tool && tool.title ? String(tool.title) : "",
      description: tool && tool.description ? String(tool.description) : "",
      inputSchema: tool && tool.inputSchema !== undefined ? tool.inputSchema : undefined,
      origin: tool && tool.origin ? String(tool.origin) : "",
      windowPresent: Boolean(tool && tool.window)
    }));
  };
  const discover = async (owner, methodName) => {
    if (!owner || typeof owner[methodName] !== "function") {
      return { attempted: false, outcome: "skipped" };
    }
    try {
      const result = await owner[methodName]();
      return { attempted: true, outcome: "success", tools: summarizeTools(result) };
    } catch (error) {
      return { attempted: true, outcome: "error", error: safeError(error) };
    }
  };
  const invoke = async (owner, methodName, tool, input, requested) => {
    if (!owner || typeof owner[methodName] !== "function") {
      return { attempted: false, outcome: "skipped", requested };
    }
    try {
      const result = await owner[methodName](tool, input);
      return { attempted: true, outcome: "success", requested, returned: result };
    } catch (error) {
      return { attempted: true, outcome: "error", requested, error: safeError(error) };
    }
  };

  const documentContext = read(() => document.modelContext);
  const navigatorContext = read(() => navigator.modelContext);
  const testingContext = read(() => navigator.modelContextTesting);
  const producer = documentContext.value || navigatorContext.value;
  const testing = testingContext.value;
  const fixture = window.__webmcpO0 || {
    ready: false,
    toolName: probeToolName,
    contextKind: "missing",
    registration: { attempted: false, outcome: "missing" },
    invocations: []
  };

  const deadline = performance.now() + 5000;
  while (!fixture.ready && performance.now() < deadline) {
    await new Promise((resolve) => setTimeout(resolve, 25));
  }

  const producerDiscovery = await discover(producer, "getTools");
  let producerTool;
  if (producerDiscovery.outcome === "success" && producerDiscovery.tools) {
    producerTool = producerDiscovery.tools.find((tool) => tool.name === probeToolName);
  }
  let producerInvocation = { attempted: false, outcome: "skipped" };
  if (producerTool && producer && typeof producer.executeTool === "function") {
    try {
      const sourceTools = await producer.getTools();
      const sourceTool = sourceTools.find((tool) => tool.name === probeToolName);
		producerInvocation = await invoke(
			producer,
			"executeTool",
			sourceTool,
			JSON.stringify({ value: "producer" }),
			probeToolName
		);
    } catch (error) {
      producerInvocation = { attempted: true, outcome: "error", requested: probeToolName, error: safeError(error) };
    }
  }

  const testingDiscovery = await discover(testing, "listTools");
  let testingToolName = missingToolName;
  if (testingDiscovery.outcome === "success" && testingDiscovery.tools &&
      testingDiscovery.tools.some((tool) => tool.name === probeToolName)) {
    testingToolName = probeToolName;
  }
  let testingInvocation = { attempted: false, outcome: "skipped", requested: testingToolName };
  if (testing && typeof testing.executeTool === "function") {
    testingInvocation = await invoke(
      testing,
      "executeTool",
      testingToolName,
      JSON.stringify({ value: "testing" }),
      testingToolName
    );
  }

  const policy = document.permissionsPolicy || document.featurePolicy;
  let originAgentCluster;
  if (typeof window.originAgentCluster === "boolean") originAgentCluster = window.originAgentCluster;
  return {
    url: location.href,
    origin: location.origin,
    isSecureContext: Boolean(window.isSecureContext),
    originAgentCluster,
    permissionsPolicyTools: policy && typeof policy.allowsFeature === "function"
      ? policy.allowsFeature("tools")
      : null,
    documentModelContext: objectReport(documentContext, [
      "registerTool", "getTools", "executeTool", "listTools", "callTool",
      "unregisterTool", "clearContext", "ontoolchange"
    ]),
    navigatorModelContext: objectReport(navigatorContext, [
      "registerTool", "getTools", "executeTool", "listTools", "callTool",
      "unregisterTool", "clearContext", "ontoolchange"
    ]),
    navigatorModelContextTesting: objectReport(testingContext, [
      "listTools", "executeTool", "getCrossDocumentScriptToolResult", "ontoolchange",
      "registerToolsChangedCallback", "getToolCalls", "reset"
    ]),
    descriptors: {
      "Document.prototype.modelContext": descriptorReport(Document, "modelContext"),
      "Navigator.prototype.modelContext": descriptorReport(Navigator, "modelContext"),
      "Navigator.prototype.modelContextTesting": descriptorReport(Navigator, "modelContextTesting")
    },
    fixture: {
      ready: Boolean(fixture.ready),
      toolName: String(fixture.toolName || probeToolName),
      contextKind: String(fixture.contextKind || "none"),
      registration: fixture.registration,
      invocations: fixture.invocations || []
    },
    producerDiscovery,
    producerInvocation,
    testingDiscovery,
    testingInvocation
  };
})()
