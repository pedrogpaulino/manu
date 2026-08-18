---
status: Accepted
date: 2026-08-17
---

# ADR 0002: Fundação Go-first do runtime

## Contexto

O primeiro incremento executável precisa oferecer uma CLI pequena e um
processo local previsível para o `Manu Agent`, antes de existirem persistência
vetorial, IA, API HTTP ou uma célula completa de implantação. A escolha deve
manter baixo o custo operacional do microcorte, permitir medição reproduzível
e não transformar cada analisador especializado em uma decisão de linguagem
irreversível.

O repositório não possui remoto Git que forneça o caminho do módulo. Para esta
fundação, o caminho canônico foi confirmado como
`github.com/pedrogpaulino/manu`. A instalação local usada na aplicação foi
validada com `go version go1.26.6 linux/amd64`; a versão `go1.26.6` também
consta como estável na [página oficial de downloads do Go](https://go.dev/dl/).

## Decisão

Adotamos Go como runtime principal do `Manu Agent`, da CLI, do pipeline comum
do `Knowledge Engine` e do backend inicial. O projeto será um módulo Go único
com o caminho `github.com/pedrogpaulino/manu`, organizado inicialmente em
`cmd/manu`, `internal` e `testdata`, sem `pkg` ou workspace multi-módulo.

A fonte do toolchain para esta aplicação e verificação é a instalação local
oficial Go 1.26.6. O `go.mod` usa `go 1.25` como versão mínima da linguagem e
`toolchain go1.26.6` como toolchain validado. Em 17/08/2026, Go 1.26 é a
versão principal corrente e Go 1.25 é a mínima ainda suportada pela
[política oficial de releases do Go](https://go.dev/doc/devel/release#policy),
que mantém uma versão até existirem duas versões principais mais novas.
Atualizações de patch entram depois de testes e benchmark, e atualizações de
versão principal repetem testes, verificação de vulnerabilidades e a linha de
base do corpus. Go não possui uma modalidade LTS.

O esqueleto executável usa somente a biblioteca padrão e APIs disponíveis
antes de Go 1.25, incluindo `runtime.Version`,
`runtime/debug.ReadBuildInfo`, `fmt`, `io` e `os`; não há dependência de API
introduzida em Go 1.26. A compilação, os testes e o `go vet` foram repetidos
com a diretiva `go 1.25` e o toolchain local `go1.26.6`.

O primeiro modo de execução é a CLI, composta por dependências explícitas na
raiz do processo e sem um container de DI. O incremento inicial não adiciona
dependências de runtime externas quando a biblioteca padrão é suficiente; não
escolhe persistência, IA, protocolo remoto, API HTTP ou Compose completo.

Go é a escolha principal, não uma obrigação para toda especialização futura.
Um analisador em outro runtime poderá ser adotado atrás de um protocolo
externo versionado somente em uma mudança própria, quando a biblioteca
especializada e as medições demonstrarem benefício que compense empacotamento,
isolamento, segurança e operação adicionais.

## Alternativas consideradas

- **Manter Go, TypeScript e Rust em paralelo:** daria liberdade imediata para
  cada camada, mas criaria múltiplas fundações e custos de operação antes de
  validar o microcorte.
- **Fixar uma versão principal antiga ou usar `latest`:** a primeira opção
  posterga correções e melhorias; a segunda reduz a reprodutibilidade. A
  política de patch verificado e toolchain fixado equilibra atualização e
  repetibilidade.
- **Começar com API HTTP, daemon ou um runtime especializado externo:**
  anteciparia ciclo de vida, transporte e isolamento antes de existir uma
  fronteira medida para o analisador. A CLI local atende o primeiro fluxo com
  menos superfície operacional.
- **Adotar um framework de DI ou biblioteca externa desde o início:**
  resolveria problemas de grafos maiores, mas o módulo inicial pode ser
  composto explicitamente sem reflexão, geração ou dependências de runtime.

## Trade-offs e consequências

- Go oferece binário único, biblioteca padrão suficiente para a fundação e
  operação local leve, favorecendo o Agent em Linux e contêiner.
- O extrator inicial pode ser menos profundo para algumas linguagens; a
  fronteira do contrato comum permanece independente do runtime e permite
  selecionar especializações futuras com evidência.
- A instalação local torna a verificação imediata e explícita, enquanto uma
  imagem de build fixada será uma preocupação posterior de distribuição.
- A ausência de dependências externas reduz superfície e custo de manutenção,
  mas deixa para mudanças futuras qualquer semântica que a biblioteca padrão
  não sustente com precisão.
- Atualizar o toolchain exige repetir verificações e a linha de base, mas
  evita que desempenho ou compatibilidade mudem silenciosamente.

## Relações

- OpenSpec: [proposta](../../openspec/changes/benchmark-select-knowledge-engine-stack/proposal.md), [design](../../openspec/changes/benchmark-select-knowledge-engine-stack/design.md) e [tarefas](../../openspec/changes/benchmark-select-knowledge-engine-stack/tasks.md)
- Documentos afetados: [`ARCHITECTURE.md`](../../ARCHITECTURE.md) e [índice de ADRs](README.md)
- ADR substituído/substituto: não aplicável
