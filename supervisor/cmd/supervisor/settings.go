package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// patchServerSettingsRCON sets .rcon.password in server-settings.json while
// leaving the rest of the document intact. Factorio's server-settings.json is a
// flat object of objects, so a decode-into-map / re-encode round-trip is safe
// (Factorio does not depend on key order).
func patchServerSettingsRCON(path, password string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return err
	}

	var rconObj map[string]json.RawMessage
	if raw, ok := doc["rcon"]; ok {
		if err := json.Unmarshal(raw, &rconObj); err != nil {
			return err
		}
	} else {
		rconObj = map[string]json.RawMessage{}
	}
	pw, err := json.Marshal(password)
	if err != nil {
		return err
	}
	rconObj["password"] = pw

	rconRaw, err := json.Marshal(rconObj)
	if err != nil {
		return err
	}
	doc["rcon"] = rconRaw

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}
