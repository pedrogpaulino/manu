## Why

O primeiro corte vertical já define corpus, perguntas e métricas, mas o repositório ainda não possui uma fundação executável nem uma decisão de runtime. Adotar Go como linguagem principal e validar um microcorte determinístico sobre o corpus permite iniciar o motor com baixo consumo e uma base simples, antes de acrescentar persistência vetorial, IA e experiências de produto.

## What Changes

- Adota Go como linguagem principal do `Manu Agent`, da CLI, do pipeline comum do `Knowledge Engine` e do backend inicial, usando a versão estável suportada mais recente e uma política explícita de atualização; Go não possui uma linha denominada LTS.
- Fixa o módulo inicial em `github.com/pedrogpaulino/manu` e valida localmente o toolchain Go 1.26.6 antes da criação do `go.mod`.
- Delimita a primeira instalação do `Manu Agent` a Linux e contêiner, com leitura local somente dos recortes autorizados, limites de recursos e sem modelo de IA ou banco principal embarcado no Agent.
- Implementa um primeiro microcorte determinístico com descoberta, hashing, inventário genérico, abertura segura de CARs, extração estrutural mínima de Java/XML/texto e projeção no contrato comum com evidências, cobertura, lacunas e falhas parciais.
- Oferece uma CLI mínima com saída humana e estruturada para analisar um recorte, inspecionar resultados e executar benchmarks de primeira análise, repetição sem mudanças e atualização localizada.
- Define uma fronteira de analisadores implementada inicialmente por interfaces Go dentro do monólito modular; analisadores futuros poderão usar outro runtime atrás de um protocolo externo versionado somente quando o ecossistema semântico e as medições justificarem o custo.
- Registra a decisão arquitetural Go-first e seus trade-offs, sem transformar Go na única linguagem permitida para toda especialização futura.
- Mantém fora deste incremento PostgreSQL/pgvector, embeddings, RAG, OpenAI, API HTTP, UI, curadoria, célula Docker Compose completa e semântica profunda de Java/Quarkus, WSO2 ou Python/Frappe.

## Capabilities

### New Capabilities

- `knowledge-engine-runtime`: define a fundação executável Go-first, a execução local segura do microcorte, a fronteira extensível de analisadores, a saída operacional da CLI e a medição reproduzível de recursos e incrementalidade.

### Modified Capabilities

Nenhuma. O incremento implementa uma primeira parte do comportamento já definido em `knowledge-engine-comprehension` sem alterar seu contrato epistemológico.

## Impact

- Introduz o primeiro módulo e código Go do repositório, testes, fixtures determinísticas, comandos de verificação e uma imagem Linux para a CLI/Agent.
- Afeta `ARCHITECTURE.md`, `README.md`, o índice de decisões e um novo ADR sobre a fundação Go-first; produto e domínio não mudam salvo se a aplicação revelar uma inconsistência conceitual.
- Registra o caminho canônico `github.com/pedrogpaulino/manu` e o toolchain local Go 1.26.6; qualquer dependência externa futura continua sujeita a aprovação antes de ser adicionada.
- Usa os caminhos externos e as revisões/hashes do corpus apenas em avaliações explícitas e somente leitura; fixtures pequenas continuam sendo a verificação padrão do código.
