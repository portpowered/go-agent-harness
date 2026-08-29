package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/portpowered/go-agent-harness/agent-cli/internal/webmcp"
	"io"
	"strings"
)

func doctorErrorDataFor(err error, fallback webmcp.ErrorCode, details map[string]any) *WebMCPDoctorErrorData {
	err = preferDirectBrowserDisconnected(err)
	result := webmcp.ResultErrorFor(err, fallback, details)
	return &WebMCPDoctorErrorData{Code: result.Code, Message: result.Message, Retryable: result.Retryable, Details: result.Details}
}

func writeWebMCPDoctorReport(out io.Writer, report WebMCPDoctorReport, asJSON bool) error {
	if out == nil {
		return errors.New("webmcp doctor output writer is required")
	}
	if asJSON {
		if err := json.NewEncoder(out).Encode(report); err != nil {
			return fmt.Errorf("write WebMCP doctor JSON: %w", err)
		}
		return nil
	}
	return writeWebMCPDoctorHuman(out, report)
}

func writeWebMCPDoctorHuman(out io.Writer, report WebMCPDoctorReport) error {
	var builder strings.Builder
	fmt.Fprintf(&builder, "WebMCP doctor: %s\n", report.Status)
	fmt.Fprintf(&builder, "Endpoint source: %s\n", report.Endpoint.Source)
	address := report.Endpoint.Address
	if address == "" {
		address = "none"
	}
	fmt.Fprintf(&builder, "Endpoint:        %s\n", address)
	fmt.Fprintf(&builder, "Scope:           %s\n", report.Endpoint.Scope)
	if len(report.Browsers) == 0 {
		builder.WriteString("Browser:         none\n")
	} else {
		for index, browser := range report.Browsers {
			label := "Browser"
			if index > 0 {
				label = fmt.Sprintf("Browser %d", index+1)
			}
			fmt.Fprintf(&builder, "%s:         %s id=%s\n", label, displayDoctorValue(browser.Product, "unknown"), browser.ID)
			fmt.Fprintf(&builder, "Protocol:        %s\n", displayDoctorValue(browser.Protocol, "unknown"))
		}
	}
	fmt.Fprintf(&builder, "WebMCP domain:   %s\n", report.WebMCP)
	fmt.Fprintf(&builder, "Page tools:      %s\n", displayDoctorValue(report.PageTools, "not_checked"))
	catalogStatus := "unverified"
	if report.Catalog.Ready {
		catalogStatus = "ready"
	}
	fmt.Fprintf(&builder, "Catalog:         %s (%d tools)\n", catalogStatus, report.Catalog.ToolCount)
	fmt.Fprintf(&builder, "Page targets:    %d\n", report.PageTargets)
	fmt.Fprintf(&builder, "Eligible pages:  %d\n", report.EligiblePages)
	if report.SelectedPage == nil {
		builder.WriteString("Selected page:   none\n")
	} else {
		fmt.Fprintf(&builder, "Selected page:   %s/%s (%q, %s)\n", report.SelectedPage.BrowserID, report.SelectedPage.TargetID, report.SelectedPage.Title, displayDoctorValue(report.SelectedPage.Origin, "origin unknown"))
	}
	builder.WriteString("Checks:\n")
	for _, check := range report.Checks {
		fmt.Fprintf(&builder, "  %-14s %s", check.Name+":", check.Status)
		if check.Message != "" {
			fmt.Fprintf(&builder, " — %s", check.Message)
		}
		builder.WriteByte('\n')
	}
	if len(report.Warnings) > 0 {
		builder.WriteString("Warnings:\n")
		for _, warning := range report.Warnings {
			fmt.Fprintf(&builder, "  - %s\n", warning)
		}
	}
	if report.Error != nil {
		fmt.Fprintf(&builder, "Error:           %s — %s\n", report.Error.Code, report.Error.Message)
	}
	if _, err := io.WriteString(out, builder.String()); err != nil {
		return fmt.Errorf("write WebMCP doctor report: %w", err)
	}
	return nil
}

func displayDoctorValue(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
