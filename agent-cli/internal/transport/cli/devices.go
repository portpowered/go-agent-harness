package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	serviceDevices "github.com/portpowered/go-agent-harness/agent-cli/internal/services/devices"
	"github.com/spf13/cobra"
)

// DevicesCommand is the devices command group.
type DevicesCommand struct{}

func NewDevicesCommand() *DevicesCommand { return &DevicesCommand{} }

func (c *DevicesCommand) Generate() *cobra.Command {
	return &cobra.Command{
		Use:     "devices",
		Short:   "Discover available audio devices",
		Long:    "Commands for discovering selectable audio input and output devices.",
		Example: "  yui devices list\n  yui devices list --json",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
}

// DevicesListCommand lists devices without opening them.
type DevicesListCommand struct {
	service serviceDevices.DeviceService
	json    bool
}

// NewDevicesListCommand constructs the transport around the thin device
// service boundary. Device discovery and selection remain in the service.
func NewDevicesListCommand(service serviceDevices.DeviceService) *DevicesListCommand {
	return &DevicesListCommand{service: service}
}

func (c *DevicesListCommand) Generate() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List available audio devices",
		Long:    "List selectable audio input and output devices and their directional defaults.",
		Example: "  yui devices list\n  yui devices list --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.run(cmd.Context(), cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&c.json, "json", false, "Write the device list as stable JSON")
	return cmd
}

type deviceListEntry struct {
	ID        string                         `json:"id"`
	Name      string                         `json:"name"`
	Direction serviceDevices.DeviceDirection `json:"direction"`
	Default   bool                           `json:"default"`
}

type deviceListResponse struct {
	Devices []deviceListEntry `json:"devices"`
}

func (c *DevicesListCommand) run(ctx context.Context, out io.Writer) error {
	if c == nil || c.service == nil {
		return errors.New("audio device service is required")
	}
	list, err := c.service.Enumerate(ctx)
	if err != nil {
		return err
	}
	entries := make([]deviceListEntry, len(list.Devices))
	for i, device := range list.Devices {
		name := device.Name
		if name == "" {
			name = device.DisplayName
		}
		entries[i] = deviceListEntry{ID: device.ID, Name: name, Direction: device.Direction, Default: device.Default}
	}
	if c.json {
		return writeDeviceListJSON(out, entries)
	}
	return writeDeviceListTable(out, entries)
}

func writeDeviceListTable(out io.Writer, entries []deviceListEntry) error {
	var table strings.Builder
	table.WriteString("INPUT\n")
	writeDeviceTableRows(&table, entries, serviceDevices.DeviceDirectionInput)
	table.WriteString("OUTPUT\n")
	writeDeviceTableRows(&table, entries, serviceDevices.DeviceDirectionOutput)
	if len(entries) == 0 {
		table.WriteString("No audio devices found.\n")
	}
	if _, err := io.WriteString(out, table.String()); err != nil {
		return fmt.Errorf("write audio device list: %w", err)
	}
	return nil
}

func writeDeviceTableRows(table *strings.Builder, entries []deviceListEntry, direction serviceDevices.DeviceDirection) {
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
