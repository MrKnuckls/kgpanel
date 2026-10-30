package version

// Version is injected at build time:
//   go build -ldflags "-X kgpanel/panel/internal/version.Version=v0.1.0"
var Version = "dev"
