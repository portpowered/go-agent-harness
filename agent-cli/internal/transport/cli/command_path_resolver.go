package cli

import (
	"fmt"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/input"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const (
	commandRouteAsk                     = "ask"
	commandRouteSession                 = "session"
	commandRouteSessionReplay           = "session replay"
	commandRouteSessionSelfPlay         = "session self-play"
	commandRouteRoomRun                 = "room run"
	commandRouteMediaProbe              = "media probe"
	commandRouteInteractionReplay       = "interaction replay"
	commandRouteProbeRun                = "probe run"
	commandRouteProbeGate               = "probe gate"
	commandRouteProbeReport             = "probe report"
	commandRouteProbeAcceptance         = "probe acceptance"
	commandRouteProbeFleet              = "probe fleet"
	commandRouteProbeCustomerSimulation = "probe customer-simulation"
)

// commandPathFlagNames is the audited CLI-owned filesystem surface for the
// interactive/session/media routes. URLs, IDs, endpoint strings, and literal
// prompt values are intentionally absent from these lists.
func commandPathFlagNames(route string) []string {
	switch route {
	case commandRouteAsk:
		return []string{"system-prompt", "record", "replay"}
	case commandRouteSession:
		return []string{
			"record",
			"record-dir",
			"replay",
			"system-prompt",
			"audio-in",
			"audio-in-turn",
			"audio-interrupt",
			"audio-out",
			"image",
			"browser-user-data-dir",
			"browser-replay",
		}
	case commandRouteSessionSelfPlay:
		return []string{"output-dir"}
	case commandRouteRoomRun:
		return []string{"config", "manifest", "replay", "out"}
	case commandRouteMediaProbe:
		return []string{"replay-fixture"}
	case commandRouteProbeRun:
		return []string{
			"scenario",
			"record",
			"replay",
			"out",
			"summary",
			"recording-root",
			"evidence-root",
			"browser-user-data-dir",
			"browser-replay",
		}
	case commandRouteProbeGate:
		return []string{"out", "json"}
	case commandRouteProbeReport:
		return []string{"out", "json", "summary"}
	case commandRouteProbeFleet:
		return []string{"manifest", "replay"}
	case commandRouteProbeCustomerSimulation:
		return []string{
			"scenario",
			"audio",
			"audio-dir",
			"patience-reprompt-audio",
			"binary",
			"run-root",
			"system-prompt",
			"secret-file",
			"validator-secret-file",
			"report",
		}
	default:
		return nil
	}
}

// commandRoute identifies a leaf command without relying on its short name;
// several route groups intentionally contain a child named "run", "probe",
// or "replay". The flag fallbacks keep independently generated commands
// usable in focused tests and by callers that embed a single route.
func commandRoute(command *cobra.Command) string {
	if command == nil {
		return ""
	}
	path := command.CommandPath()
	for _, route := range []string{
		commandRouteSessionReplay,
		commandRouteSessionSelfPlay,
		commandRouteInteractionReplay,
		commandRouteProbeCustomerSimulation,
		commandRouteProbeAcceptance,
		commandRouteProbeReport,
		commandRouteProbeGate,
		commandRouteProbeFleet,
		commandRouteProbeRun,
		commandRouteRoomRun,
		commandRouteMediaProbe,
		commandRouteAsk,
		commandRouteSession,
	} {
		if path == route || strings.HasSuffix(path, " "+route) {
			return route
		}
	}

	switch command.Name() {
	case "ask":
		return commandRouteAsk
	case "session":
		return commandRouteSession
	case "customer-simulation":
		return commandRouteProbeCustomerSimulation
	case "acceptance", "accept":
		return commandRouteProbeAcceptance
	case "gate":
		return commandRouteProbeGate
	case "report":
		return commandRouteProbeReport
	case "fleet":
		return commandRouteProbeFleet
	case "self-play":
		return commandRouteSessionSelfPlay
	case "run":
		if command.Flags().Lookup("config") != nil && command.Flags().Lookup("out") != nil {
			return commandRouteRoomRun
		}
		if command.Flags().Lookup("scenario") != nil && command.Flags().Lookup("replay") != nil {
			return commandRouteProbeRun
		}
	case "probe":
		if command.Flags().Lookup("replay-fixture") != nil {
			return commandRouteMediaProbe
		}
	case "replay":
		if command.Flags().Lookup("replay-fixture") != nil {
			return commandRouteMediaProbe
		}
		return commandRouteInteractionReplay
	}
	return ""
}

type commandPathFlagUpdate struct {
	flag   *pflag.Flag
	slice  pflag.SliceValue
	values []string
}

func resolveCommandPathFlag(resolver *pathResolver, flag *pflag.Flag) (commandPathFlagUpdate, error) {
	update := commandPathFlagUpdate{flag: flag}
	if flag == nil || flag.Value == nil {
		return update, fmt.Errorf("path flag is not configured")
	}
	if slice, ok := flag.Value.(pflag.SliceValue); ok {
		update.slice = slice
		update.values = append([]string(nil), slice.GetSlice()...)
	} else {
		update.values = []string{flag.Value.String()}
	}
	for index, value := range update.values {
		resolved, err := resolver.Resolve(value)
		if err != nil {
			if len(update.values) > 1 {
				return commandPathFlagUpdate{}, fmt.Errorf("resolve --%s value %d: %w", flag.Name, index+1, err)
			}
			return commandPathFlagUpdate{}, fmt.Errorf("resolve --%s: %w", flag.Name, err)
		}
		update.values[index] = resolved
	}
	return update, nil
}

func (update commandPathFlagUpdate) apply() error {
	if update.slice != nil {
		return update.slice.Replace(update.values)
	}
	return update.flag.Value.Set(update.values[0])
}

// resolveAskFileArguments resolves only the positional arguments that the ask
// parser regards as attachment paths. Prompt text remains byte-for-byte
// untouched, while the normalized paths are put back in their original
// positions before AskCommand parses the arguments again.
func resolveAskFileArguments(resolver *pathResolver, args []string) ([]string, error) {
	_, rawPaths := input.ParseAskArgs(args)
	if len(rawPaths) == 0 {
		return nil, nil
	}

	resolvedPaths := make([]string, len(rawPaths))
	for index, rawPath := range rawPaths {
		resolved, err := resolver.Resolve(rawPath)
		if err != nil {
			return nil, fmt.Errorf("resolve ask file operand %d: %w", index+1, err)
		}
		resolvedPaths[index] = resolved
	}

	normalized := append([]string(nil), args...)
	pathIndex := 0
	for index, arg := range args {
		if pathIndex >= len(rawPaths) || arg != rawPaths[pathIndex] {
			continue
		}
		normalized[index] = resolvedPaths[pathIndex]
		pathIndex++
	}
	if pathIndex != len(rawPaths) {
		return nil, fmt.Errorf("resolve ask file operands: parser returned an inconsistent operand list")
	}
	return normalized, nil
}

// resolvePathArguments resolves positional filesystem operands transactionally.
// Probe run and customer-simulation accept scenario paths as positional
// operands, while acceptance accepts one executable followed by literal goal
// text. Callers choose the count so a prompt or other non-path operand remains
// unchanged.
func resolvePathArguments(resolver *pathResolver, args []string, count int, label string) ([]string, error) {
	if count <= 0 || len(args) == 0 {
		return nil, nil
	}
	if count > len(args) {
		count = len(args)
	}
	normalized := append([]string(nil), args...)
	for index := 0; index < count; index++ {
		resolved, err := resolver.Resolve(args[index])
		if err != nil {
			return nil, fmt.Errorf("resolve %s operand %d: %w", label, index+1, err)
		}
		normalized[index] = resolved
	}
	return normalized, nil
}

// resolveCommandPaths performs all route-specific path resolution before a
// command's PreRunE or RunE. It computes every replacement first, making a
// failed repeatable value a pure validation failure with no partial command
// state applied and no command-specific side effects started.
func (r *Router) resolveCommandPaths(command *cobra.Command, args []string) error {
	if r == nil || command == nil {
		return nil
	}
	resolver := r.pathResolver
	if resolver == nil {
		resolver = newPathResolver()
	}
	route := commandRoute(command)
	updates, err := resolveCommandPathFlags(resolver, command, route)
	if err != nil {
		return err
	}
	normalizedArgs, err := resolveCommandArguments(resolver, route, args)
	if err != nil {
		return err
	}
	for _, update := range updates {
		if err := update.apply(); err != nil {
			return fmt.Errorf("apply normalized --%s: %w", update.flag.Name, err)
		}
	}
	if normalizedArgs != nil {
		copy(args, normalizedArgs)
	}
	return nil
}

// resolveCommandPathFlags computes the normalized value of every changed path
// flag for the route without applying any of them.
func resolveCommandPathFlags(resolver *pathResolver, command *cobra.Command, route string) ([]commandPathFlagUpdate, error) {
	names := commandPathFlagNames(route)
	updates := make([]commandPathFlagUpdate, 0, len(names))
	for _, name := range names {
		flag := command.Flags().Lookup(name)
		if flag == nil || !flag.Changed {
			continue
		}
		update, err := resolveCommandPathFlag(resolver, flag)
		if err != nil {
			return nil, err
		}
		updates = append(updates, update)
	}
	return updates, nil
}

// resolveCommandArguments returns the route's normalized positional path
// arguments, or nil when the route has none to normalize.
func resolveCommandArguments(resolver *pathResolver, route string, args []string) ([]string, error) {
	switch {
	case route == commandRouteAsk:
		return resolveAskFileArguments(resolver, args)
	case route == commandRouteProbeRun || route == commandRouteProbeCustomerSimulation:
		return resolvePathArguments(resolver, args, len(args), "scenario")
	case route == commandRouteProbeAcceptance:
		return resolvePathArguments(resolver, args, 1, "acceptance executable")
	case (route == commandRouteInteractionReplay || route == commandRouteSessionReplay) && len(args) == 1:
		resolved, err := resolver.Resolve(args[0])
		if err == nil {
			return []string{resolved}, nil
		}
		if route == commandRouteSessionReplay {
			return nil, fmt.Errorf("resolve session replay bundle: %w", err)
		}
		return nil, fmt.Errorf("resolve interaction replay fixture: %w", err)
	}
	return nil, nil
}
