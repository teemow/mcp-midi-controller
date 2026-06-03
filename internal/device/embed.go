package device

import "embed"

// bundledFS holds the device definitions shipped inside the binary. Source of
// truth lives next to this file under definitions/. User definitions in the
// config dir override these by definition id (the `id:` field), not by
// filename: LoadDir keys the registry on id, so a user file with the same id
// replaces the bundled definition whatever the file is called.
//
//go:embed definitions/*.yaml
var bundledFS embed.FS
