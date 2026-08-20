# Linha de base da avaliação do primeiro corte — tarefa 9.5

Este documento explica a linha de base versionada em
[first-vertical-slice-9-5.json](first-vertical-slice-9-5.json). O JSON é o
registro estruturado e deve ser preservado junto com esta explicação; este
arquivo não repete conteúdo das fontes avaliadas.

A linha de base foi registrada em 2026-08-20 a partir da execução local real
descrita em [first-vertical-slice-real-9-4.md](first-vertical-slice-real-9-4.md).
Ela é uma referência experimental do microcorte, não uma promessa de
desempenho, capacidade comercial ou SLA.

## Identidade e ambiente

- `baseline_id`: `first-vertical-slice-9-5-20260820`.
- `run_id`: `real-9-4-ticketmaster-20260820`.
- Modo: `simulated`, sem rede e sem chamada a provedor externo.
- Engine: `evaluation-real-extractor-v1`; casos, bundle e contrato:
  `v1alpha1`.
- Ambiente: Go `go1.26.6`, Linux `amd64`, CPU AMD Ryzen 9 5900X 12-Core
  Processor, 24 CPUs e `GOMAXPROCS=24`.
- Digest de reprodutibilidade produzido pelo runner:
  `a93d11940cbd7b29ddc3b25825806e2cea5f2e607f76516eb3b6d1f15193b818`.

As fontes foram usadas somente para leitura. A identidade das três revisões e
a seleção dos seis CARs WSO2 ficam no JSON por `source_revision`; os hashes
SHA-256 dos CARs selecionados também são registrados em
`configuration.corpora[].artifact_hashes`. Nenhum caminho privado é requisito
para interpretar ou validar a linha de base.

## Configuração e perfis

A execução usou `top_k=5`, repetição habilitada, organização sintética local,
concorrência máxima 4, limite de 10.000 arquivos, 1 GiB de leitura e limites
de evidência de oito unidades por artefato, 4 KiB por unidade e 2.048
caracteres por unidade. Os limites completos, inclusões e revisões estão no
JSON.

A política permitiu persistência local, mas negou `external_transfer`. Conteúdo
sensível, injeção de prompt e binário não foram autorizados para transferência.
Por isso:

- o perfil de embedding registrado é o simulado, dimensão 8, e a projeção real
  ficou desabilitada pela política;
- o perfil de geração é o simulado, mas não foi chamado para o corpus real,
  pois não havia pacote transferível;
- não há perfis externos, aliases móveis, credenciais ou chaves no registro.

Essa configuração mantém a execução determinística e permite distinguir
ausência de embedding, abstinência e falha de recuperação sem abrir a política
para fazer o caso passar.

## Métricas registradas

O resumo da execução foi:

| Métrica | Valor |
| --- | ---: |
| Casos / concluídos / falhos | 4 / 0 / 4 |
| Abstinências esperadas / corretas | 1 / 1 |
| `evidence_recall_at_k` médio (`k=5`) | 0 |
| `evidence_precision_at_k` médio | 0 |
| Claims válidos / citações válidas | 4 / 0 |
| Conteúdo limitado observado | 108.472 bytes |
| Evidências reutilizadas / reprocessadas | 1.696 / 1.696 |

Os quatro casos Ticketmaster chegaram a extração, ingestão e recuperação. Cada
um produziu cinco candidatos textuais, nenhum candidato vetorial e a razão de
degradação `vector_unavailable`. O recall zero é uma falha observada de
recuperação para os locadores atuais, não uma evidência criada a partir da
rubrica. `TM-ABS-01` foi a única abstinência esperada; as abstinências dos
outros três casos não foram contadas como corretas. A política foi registrada
separadamente como `limited/transfer_not_authorized`, com 424 unidades
bloqueadas por caso.

As medições de extração dos três recortes foram registradas como inventário
bounded, sem afirmar cobertura completa:

| Fonte | Artefatos | Contribuições | Evidências | Conteúdo limitado | Digest factual |
| --- | ---: | ---: | ---: | ---: | --- |
| Ticketmaster | 65 | 1.086 | 424 | 27.118 bytes | `8a31aaa1f9dee2c4b12db96a1361d33c511f41363e2962a5cf1cf3be2e01a16b` |
| WSO2, seis CARs | 6 | 644 | 38 | 1.054 bytes | `7a9c3f5e72092fe1293f9dc3abe60aa845f3e77bb309c653d0439ad7a79c2cae` |
| ERPNext | 5.316 | 10.582 | 5.319 | 1.043.377 bytes | `34e967ba0dda1a3b710d6658cc001a525efa5d162700a4d0ce384980cf2d35e6` |

As etapas posteriores de WSO2 e ERPNext foram marcadas como não aplicáveis
porque não havia casos de competência conectados a elas nesta execução. Isso
não equivale a sucesso nem a compreensão semântica completa.

## Transferência, custos e integridade

`content_transfer.policy` é `deny`, com zero chamadas externas. Portanto,
`transferred_evidence` e `transferred_content_hashes` são listas vazias. O
relatório registra `secret_present=false` e `raw_content_recorded=false`; uma
execução `live` futura deverá registrar somente o identificador e o hash de
cada evidência autorizada, nunca o conteúdo integral ou a credencial.

O custo de provedor externo é `not_applicable`, com zero chamadas e tokens/custo
nulos porque não houve provider nem tabela de preços aplicada. A simulação
local também não mediu custo de máquina. `null` nesse contexto significa “não
medido”, não custo zero.

Os tempos por etapa estão no JSON apenas como observações locais. Eles são
explicitamente excluídos do digest de reprodutibilidade, junto com timestamps
de parede, nome do host e caminhos privados. Assim, a integridade da linha de
base depende de revisões, configuração, perfis, métricas não temporais,
política e digests factuais, e não de latências voláteis.

Não foi feita comparação entre modelos. O registro declara uma única linha de
base; uma comparação futura só será válida se mantiver revisões, seleção,
limites, casos, versões, política, ambiente e modo equivalentes. Comparar um
modelo externo com o provider simulado, ou comunicar o tempo desta máquina
como SLA, é explicitamente inválido.

## Limitações

- A extração foi limitada e algumas unidades foram redigidas ou omitidas.
- WSO2 representa seis CARs de uma amostra maior e não cobre semântica profunda
  ou execução de runtime.
- ERPNext foi medido com limites bounded; o resultado não comprova inventário
  integral de uma árvore de aproximadamente 1,9 GiB.
- O Ticketmaster revelou recall textual insuficiente para os quatro conjuntos
  de locadores deste primeiro corte.
- Não foram executados provider externo, live eval, banco externo, runtime
  WSO2, site ERPNext ou ambiente de execução do Ticketmaster.

Essas limitações são parte da baseline e devem acompanhar qualquer relatório
que a cite.
