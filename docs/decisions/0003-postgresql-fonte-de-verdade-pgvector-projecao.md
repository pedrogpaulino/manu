---
status: Accepted
date: 2026-08-18
---

# ADR 0003: PostgreSQL como fonte de verdade e pgvector como projeção inicial

## Contexto

O primeiro fluxo consultável precisa preservar os fatos, observações,
relações, evidências, cobertura, lacunas e histórico de uma `Organization`
antes de preparar índices para recuperação. O `Manu Agent` continua sendo um
produtor local, determinístico e independente de banco; a plataforma precisa
receber seu bundle sem transformar uma projeção de busca na representação do
conhecimento.

Embeddings são úteis para a recuperação semântica, mas dependem de perfil,
modelo e configuração que podem mudar. Se o vetor fosse a fonte de verdade,
trocar o perfil exigiria reingerir fatos e poderia confundir uma aproximação
de busca com `Evidence` ou conhecimento observado. A célula inicial também
precisa de transações, relações consultáveis, escopo por `Organization` e
reconstrução identificável de visões derivadas.

## Decisão

Adotamos PostgreSQL como a fonte de verdade operacional do primeiro corte
consultável. A representação canônica deve preservar, no mínimo, a
`Organization`, `Source`s, `Analysis Snapshot`s, `Artifact`s,
`Observation`s/contribuições, `Entity`s, `Relationship`s, `Evidence`,
`Analysis Coverage`, `Explicit Gap`s, falhas e o estado das operações de
ingestão e consulta. Snapshots são imutáveis; uma visão ativa pode avançar
para um novo snapshot sem apagar os anteriores.

Adotamos pgvector somente como a projeção vetorial inicial dessa representação.
Projeções vetoriais, textuais e relacionais são substituíveis e reconstruíveis
a partir dos dados canônicos autorizados. Embeddings são dados derivados de
recuperação: não são a fonte de verdade, não são `Evidence` por si sós e não
podem substituir observações, relações ou proveniência. Cada perfil de
embedding é identificável; trocar o perfil cria uma nova projeção e exige
rebuild, sem misturar vetores incompatíveis na mesma consulta.

Essa decisão orienta a persistência do primeiro vertical slice e da célula
local prevista. Ela não fixa o modelo físico do domínio, não promete uma
implantação de produção e não transforma migrações, API, Compose ou consultas
em capacidades já existentes no estado atual do repositório. O Agent continua
sem conexão direta com PostgreSQL e sem dependência de IA.

## Alternativas consideradas

- **SQLite:** reduziria o consumo de uma execução local, mas não validaria a
  concorrência da plataforma nem a projeção pgvector prevista para o corte.
- **Banco vetorial dedicado:** poderia otimizar similaridade, porém adicionaria
  outro serviço e outra fonte operacional antes de existir necessidade medida.
- **Bundle/JSON como persistência consultável:** manteria portabilidade, mas
  não ofereceria transações, relações e consultas concorrentes adequadas à
  plataforma.

## Trade-offs e consequências

- PostgreSQL oferece uma fronteira transacional única para fatos canônicos,
  histórico e escopo lógico de `Organization`, ao custo de mais consumo local
  que uma persistência embutida.
- pgvector permite a primeira busca semântica na mesma célula e pode ser
  removido e reconstruído sem alterar o conhecimento canônico.
- A disponibilidade de embeddings deixa de bloquear todo o conhecimento:
  recuperação textual e relacional pode continuar, com a degradação explícita.
- Migrações, limites de escopo e reconstrução de projeções precisam ser
  tratados como contratos operacionais; mudar a infraestrutura depois exige
  preservar a mesma fronteira de fonte de verdade e proveniência.

## Relações

- OpenSpec: [design da mudança](../../openspec/changes/define-first-evidence-backed-query-slice/design.md), [especificação da capacidade](../../openspec/changes/define-first-evidence-backed-query-slice/specs/evidence-backed-query/spec.md)
- Documentos afetados: [`ARCHITECTURE.md`](../../ARCHITECTURE.md), [índice de ADRs](README.md)
- ADR substituído/substituto: não aplicável
