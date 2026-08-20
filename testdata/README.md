# Fixtures

Este diretório guarda fixtures pequenas, determinísticas e não sensíveis para
os testes do runtime. O comando `version` não precisa de fixture; os recortes
do pipeline serão adicionados nas tarefas correspondentes.

## Casos de avaliação

[`evaluation/cases.json`](evaluation/cases.json) é o envelope `v1alpha1` de
casos em rascunho para a fixture `fixture-analyzers` e para o recorte Ticketmaster.
Ele contém somente perguntas, claims aceitáveis em modo semântico, locadores,
padrões de metadados, lacunas e atribuição de falhas por etapa; não contém
código-fonte, conteúdo bruto, segredos ou caminhos absolutos. A revisão
registrada é técnica e automatizada; os casos ainda não foram aprovados como
baseline curada por revisão humana.
Os registros identificam a autoria como `manu-change-9-1` e a revisão como
`manu-contract-validation`.

A fixture usa a revisão do repositório `2b08cbdd9ff398d135fa103ccbeaf84e1c774233`
e locadores relativos como `analyzers/Sample.java` e
`contract/manifest.golden.json`. O Ticketmaster usa a revisão documentada
`88cab04c59c58e745a94302e5c9e856830c4c902`; seus locadores relativos seguem o
[protocolo do primeiro corte](../docs/evaluation/first-vertical-slice-evaluation.md).
