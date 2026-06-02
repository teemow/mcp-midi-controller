package device

import "embed"

// bundledFS holds the device definitions shipped inside the binary. Source of
// truth lives next to this file under definitions/. User definitions in the
// config dir override these by filename.
//
//go:embed definitions/*.yaml
var bundledFS embed.FS
