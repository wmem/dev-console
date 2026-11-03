// Package deploy保存随二进制分发的systemd模板。
package deploy

import _ "embed"

//go:embed dconsole.service
var Service string
