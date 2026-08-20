// Package ingestion coordena a validação e o processamento de Analysis
// Bundles sem transformar o Manu Agent em servidor ou cliente de banco.
//
// A direção de dependência é da ingestão para bundle, evidence e persistence;
// integrações opcionais passam por portas internas. O pacote não conhece
// roteamento HTTP, comandos CLI, caminhos da fonte ou detalhes de provedor.
package ingestion
