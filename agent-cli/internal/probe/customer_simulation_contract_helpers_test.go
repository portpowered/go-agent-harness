package probe

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func WriteCustomerScenario(path string, scenario CustomerScenario) error {
	if err := scenario.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(scenario, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), privateDirMode); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), privateFileMode)
}
