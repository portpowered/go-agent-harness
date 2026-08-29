package cli

import "errors"

func (c *WebMCPOperationsCommand) loadDirectSelection() (WebMCPSelection, error) {
	store, err := c.selectionStore()
	if err != nil {
		return WebMCPSelection{}, err
	}
	return store.Load()
}

func (c *WebMCPOperationsCommand) saveDirectSelection(selection WebMCPSelection) error {
	store, err := c.selectionStore()
	if err != nil {
		return err
	}
	return store.Save(selection)
}

func (c *WebMCPOperationsCommand) selectionStore() (WebMCPSelectionStore, error) {
	if c != nil && c.SelectionStore != nil {
		return c.SelectionStore, nil
	}
	configDir := ""
	if c != nil && c.globalFlags != nil {
		configDir = c.globalFlags.ConfigDir()
	}
	store := NewFileWebMCPSelectionStore(configDir)
	if store.Path == "" {
		return nil, errors.New("WebMCP selection store is unavailable")
	}
	return store, nil
}
