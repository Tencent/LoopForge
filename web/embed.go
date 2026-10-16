package web

import "embed"

//go:embed boot.js i18n.js i18n-en.js preferences.css login.html login.js bot-bind.html bot-bind.js index.html vscode.html vscode.js vscode.css app.js app assistant.js assistant.css collaboration.js collaboration.css orchestrate.js collab.css deployment-dashboard.css metrics-dashboard.css business-domains.css bots.css knowledge.js knowledge.css mcp-setup.txt styles.css assets.css platform.css observability.css artifacts.css sops.css sync.css access.css product-v3.css product-v4.css images/credentials/*.svg images/loopforge-logo.png favicon.ico favicon.png mcp-icons monaco vscode-web extensions
var Files embed.FS
