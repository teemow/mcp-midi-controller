package device

import "embed"

// bundledFS holds the device types shipped inside the binary. Source of
// truth lives next to this file under device-types/. User device types in the
// config dir override these by device-type id (the `id:` field), not by
// filename: LoadDir keys the registry on id, so a user file with the same id
// replaces the bundled device type whatever the file is called.
//
//go:embed device-types/*.yaml
var bundledFS embed.FS
