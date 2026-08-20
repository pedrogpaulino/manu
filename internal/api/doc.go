// Package api define a superfície HTTP versionada da plataforma local.
//
// A API chama as portas de ingestion, retrieval e evidence e traduz seus
// resultados para contratos HTTP. Ela não acessa PostgreSQL, fontes,
// analisadores ou provedores diretamente e não altera o Agent existente.
package api
