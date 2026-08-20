// Package persistence representa a fronteira da fonte de verdade operacional
// e de suas projeções reconstruíveis.
//
// A direção é da persistência para os modelos de bundle/evidência e para o
// adaptador PostgreSQL; a camada não conhece API, CLI, analisadores ou
// provedores de IA. SQL, transações, migrações e o driver serão introduzidos
// pelas tarefas que consumirem esta fronteira.
package persistence
