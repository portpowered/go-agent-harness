package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/portpowered/go-agent-harness/agent-cli/internal/audio"
	"github.com/spf13/cobra"
)

var deviceListDirections = [...]audio.Direction{audio.DirectionInput, audio.DirectionOutput}

// DevicesCommand is the devices command group.
type DevicesCommand struct{}

func NewDevicesCommand() *DevicesCommand { return &DevicesCommand{} }

func (c *DevicesCommand) Generate() *cobra.Command {
	return &cobra.Command{
		Use:   "devices",
		Short: "Discover available audio devices",
		Long:  "Commands for discovering selectable audio input and output devices.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
}

// DevicesListCommand lists devices without opening them.
type DevicesListCommand struct {
	registry audio.DeviceRegistry
	json     bool
}

func NewDevicesListCommand(registry audio.DeviceRegistry) *DevicesListCommand {
	return &DevicesListCommand{registry: registry}
}

func (c *DevicesListCommand) Generate() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List available audio devices",
		Long:  "List selectable audio input and output devices and their directional defaults.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.run(cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&c.json, "json", false, "Write the device list as stable JSON")
	return cmd
}

type deviceListEntry struct {
	ID        audio.DeviceID  `json:"id"`
	Name      string          `json:"name"`
	Direction audio.Direction `json:"direction"`
	Default   bool            `json:"default"`
}

type deviceListResponse struct {
	Devices []deviceListEntry `json:"devices"`
}

func (c *DevicesListCommand) run(out io.Writer) error {
	entries, err := snapshotDeviceList(c.registry)
	if err != nil {
		return err
	}
	if c.json {
		return writeDeviceListJSON(out, entries)
	}
	return writeDeviceListTable(out, entries)
}

func snapshotDeviceList(registry audio.DeviceRegistry) ([]deviceListEntry, error) {
	if registry == nil {
		return nil, errors.New("audio device registry is required")
	}

	devices, err := registry.List()
	if err != nil {
		return nil, fmt.Errorf("list audio devices: %w", err)
	}
	entries := make([]deviceListEntry, 0, len(devices))
	seen := make(map[string]struct{}, len(devices))
	for _, device := range devices {
		if err := device.Validate(); err != nil {
			return nil, fmt.Errorf("list audio device %q: %w", device.ID, err)
		}
		key := string(device.ID) + "\x00" + device.Direction.String()
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("list audio devices: duplicate device %q for %s", device.ID, device.Direction)
		}
		seen[key] = struct{}{}
		entries = append(entries, deviceListEntry{
			ID:        device.ID,
			Name:      device.Display(),
			Direction: device.Direction,
		})
	}

	// An empty registry has no directional defaults to resolve.
	if len(entries) == 0 {
		return entries, nil
	}

	for _, direction := range deviceListDirections {
		defaultDevice, err := registry.Default(direction)
		if err != nil {
			return nil, fmt.Errorf("resolve default %s audio device: %w", direction, err)
		}
		if err := defaultDevice.Validate(); err != nil {
			return nil, fmt.Errorf("resolve default %s audio device: %w", direction, err)
		}
		if defaultDevice.Direction != direction {
			return nil, fmt.Errorf("resolve default %s audio device: registry returned %s device %q", direction, defaultDevice.Direction, defaultDevice.ID)
		}
		found := false
		for i := range entries {
			if entries[i].ID == defaultDevice.ID && entries[i].Direction == direction {
				entries[i].Default = true
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("resolve default %s audio device: %q was not returned by enumeration", direction, defaultDevice.ID)
		}
	}

	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Direction != entries[j].Direction {
			return entries[i].Direction == audio.DirectionInput
		}
		if entries[i].ID != entries[j].ID {
			return entries[i].ID < entries[j].ID
		}
		return entries[i].Name < entries[j].Name
	})
	return entries, nil
}

func writeDeviceListTable(out io.Writer, entries []deviceListEntry) error {
	var table strings.Builder
	table.WriteString("INPUT\n")
	writeDeviceTableRows(&table, entries, audio.DirectionInput)
	table.WriteString("OUTPUT\n")
	writeDeviceTableRows(&table, entries, audio.DirectionOutput)
	if len(entries) == 0 {
		table.WriteString("No audio devices found.\n")
	}
	if _, err := io.WriteString(out, table.String()); err != nil {
		return fmt.Errorf("write audio device list: %w", err)
	}
	return nil
}

func writeDeviceTableRows(table *strings.Builder, entries []deviceListEntry, direction audio.Direction) {
	for _, entry := range entries {
		if entry.Direction != direction {
			continue
		}
		marker := ""
		if entry.Default {
			marker = "default"
		}
		fmt.Fprintf(table, "  %-7s id=%s %q\n", marker, entry.ID, entry.Name)
	}
}

func writeDeviceListJSON(out io.Writer, entries []deviceListEntry) error {
	response := deviceListResponse{Devices: entries}
	if err := json.NewEncoder(out).Encode(response); err != nil {
		return fmt.Errorf("write audio device JSON: %w", err)
	}
	return nil
}
