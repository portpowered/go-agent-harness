package cli

import (
	"errors"
	"fmt"
)

func newWebMCPDoctorReport() WebMCPDoctorReport {
	report := WebMCPDoctorReport{
		Version:      doctorResultVersion,
		Status:       doctorStatusNotReady,
		Warnings:     []string{},
		Checks:       make([]WebMCPDoctorCheck, 0, 11),
		Browsers:     []WebMCPDoctorBrowser{},
		Targets:      []WebMCPDoctorTarget{},
		WebMCP:       "not_checked",
		WebMCPDomain: "not_checked",
		PageTools:    "not_checked",
	}
	for _, name := range []string{"configuration", "activation", "endpoint", "discovery", "version", "targets", "selection", "policy", "webmcp", "catalog", "cleanup"} {
		report.Checks = append(report.Checks, WebMCPDoctorCheck{Name: name, Status: doctorCheckSkipped})
	}
	return report
}

func (r *WebMCPDoctorReport) setCheck(name, status, message string, details map[string]any) {
	if r == nil {
		return
	}
	for index := range r.Checks {
		if r.Checks[index].Name != name {
			continue
		}
		r.Checks[index] = WebMCPDoctorCheck{Name: name, Status: status, Message: message, Details: details}
		return
	}
	r.Checks = append(r.Checks, WebMCPDoctorCheck{Name: name, Status: status, Message: message, Details: details})
}

func (r *WebMCPDoctorReport) addWarning(warning string) {
	if r == nil || warning == "" || len(r.Warnings) >= 8 {
		return
	}
	for _, existing := range r.Warnings {
		if existing == warning {
			return
		}
	}
	r.Warnings = append(r.Warnings, boundedDoctorText(warning, 240))
}

func closeWebMCPDoctorRuntime(runtime WebMCPDoctorRuntime) (err error) {
	if runtime.Broker != nil {
		err = errors.Join(err, callDoctorClose(runtime.Broker.Close))
	}
	if runtime.Close != nil {
		// The runtime hook owns resources outside the broker (for example a
		// version-endpoint client or an adapter transport), so it is called
		// independently and its failure is joined with broker cleanup.
		err = errors.Join(err, callDoctorClose(runtime.Close))
	}
	return err
}

func callDoctorClose(closeFunc func() error) (err error) {
	if closeFunc == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("doctor cleanup panicked")
		}
	}()
	return closeFunc()
}
