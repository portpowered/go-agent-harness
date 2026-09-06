package webmcp

import (
	runtimeTools "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools"
	runtimeToolsWire "github.com/portpowered/go-agent-harness/go-agent-runtime/services/tools/wire"
)

// BrokerToolDefinition is kept as a package-local compatibility name while
// the provider-facing browser tool contract is owned by the reusable tools
// service. The CLI package supplies only broker and platform adapters.
type BrokerToolDefinition = runtimeTools.BrokerToolDefinition

const (
	GetContextToolName      = runtimeTools.GetContextToolName
	ListTabsToolName        = runtimeTools.ListTabsToolName
	SelectTabToolName       = runtimeTools.SelectTabToolName
	ListToolsToolName       = runtimeTools.ListToolsToolName
	InvokeToolName          = runtimeTools.InvokeToolName
	CancelToolName          = runtimeTools.CancelToolName
	OpenTabToolName         = runtimeTools.OpenTabToolName
	NavigateTabToolName     = runtimeTools.NavigateTabToolName
	ShowPageToolName        = runtimeTools.ShowPageToolName
	ListCastDevicesToolName = runtimeTools.ListCastDevicesToolName
	CastTabToolName         = runtimeTools.CastTabToolName
	StopCastingToolName     = runtimeTools.StopCastingToolName
)

func StableBrokerToolDefinitions() []BrokerToolDefinition {
	return browserContract().StableBrokerToolDefinitions()
}

func StableBrokerToolSchemas() []map[string]any {
	return browserContract().StableBrokerToolSchemas()
}

func BrowserToolDefinitions(webCast ...bool) []BrokerToolDefinition {
	return browserContract().BrowserToolDefinitions(webCast...)
}

func BrowserToolSchemas(webCast ...bool) []map[string]any {
	return browserContract().BrowserToolSchemas(webCast...)
}

func BrokerToolDefinitions() []BrokerToolDefinition {
	return browserContract().BrokerToolDefinitions()
}

func StableToolNames() []string {
	return browserContract().StableToolNames()
}

func browserContract() runtimeTools.BrowserContract {
	return runtimeToolsWire.NewService().BrowserContract()
}
