# Avaliação local com fontes reais — tarefa 9.4

Este registro documenta uma execução local, determinística e sem chamadas a
provedor da tarefa 9.4. Ele usa os analisadores já existentes do Manu para
produzir bundles reais e, quando há casos aplicáveis, os entrega ao pipeline
local de avaliação. Não é um baseline curado, não é uma avaliação `live` e as
latências deste registro não são SLA.

## Reprodutibilidade e segurança

Execução observada em 2026-08-20, Linux amd64, Go `go1.26.6`, com limites
explícitos e configuração fornecida fora do repositório:

```text
MANU_REAL_EVAL_CONFIG=/tmp/manu-real-eval-9-4.json \
GOCACHE=/tmp/manu-go-cache \
go test -v ./internal/evaluation -run '^TestConfiguredRealCorpora$' -count=1
```

A configuração exigiu `corpus_id`, `corpus_revision`, `source_id`,
`source_revision`, organização, raiz absoluta, inclusões, limites e política.
O default de política manteve `external_transfer=deny`; nenhum conteúdo foi
enviado à OpenAI API ou a outro provedor. Para respeitar essa política, a
ingestão local ativou as projeções canônica e textual e marcou a projeção
vetorial como indisponível. Embeddings simulados continuam cobertos somente
pelas fixtures locais autorizadas do runner.

Limites comuns da execução: 10.000 arquivos, 1 GiB de leitura total, 256 MiB
por arquivo, quatro trabalhadores, 1 MiB de extração e oito unidades por
artefato, com 4 KiB por unidade e 2 Ki caracteres por unidade. O limite é uma
fronteira de medição, não uma alegação de cobertura integral.

Os relatórios do runner contêm apenas IDs, hashes, contagens, estados,
métricas e motivos de limitação. Não contêm caminho absoluto, segredo,
conteúdo de fonte ou diagnóstico bruto.

## Identidade das fontes verificada

| Fonte | Revisão usada | Papel | Verificação somente leitura |
| --- | --- | --- | --- |
| `ticketmaster-java-quarkus` | `88cab04c59c58e745a94302e5c9e856830c4c902` | referência Java/Quarkus | `HEAD` coincidiu com o manifesto canônico; `git status --porcelain` vazio |
| `wso2-car-sample` | `23eca6b8f6efdb9e8e671678953c983d6f911d614ca539f5d397c545452a3943` | heterogeneidade declarativa | 132 CARs observados; os seis CARs selecionados coincidiram com os hashes do manifesto |
| `erpnext-inventory` | `1f839061899c019b9a326b960fc5d10b4b34c761` | inventário e escala | `HEAD` coincidiu com o manifesto canônico; `git status --porcelain` vazio |

Os seis hashes WSO2 confirmados foram `f2336823…0720f`, `7c14d812…db61b`,
`ca58026f…3048`, `d8eb7154…a937`, `0ce30520…c63f` e `9d54deef…8999`, na
mesma seleção do [manifesto do corpus](first-vertical-slice-corpus.md).

## Resultado do Ticketmaster

Foram selecionados quatro casos reais (`TM-ABS-01`, `TM-FLOW-01`, `TM-INV-01`
e `TM-PROV-01`). O extrator não fabricou evidência a partir da rubrica:
analisou a árvore local com os analisadores genérico e Java e mapeou locadores
somente quando uma unidade real os sustentava.

| Etapa | Resultado observado |
| --- | --- |
| Extração | concluída nos quatro casos; 65 artefatos, 1.086 contribuições, 424 unidades e 27.118 bytes de conteúdo limitado por bundle |
| Ingestão | concluída nos quatro casos; projeções canônica/textual ativas e 424 unidades reutilizadas na repetição |
| Recuperação | concluída, mas com cinco candidatos textuais por caso, zero candidatos vetoriais e `vector_unavailable` |
| Recall de evidência | `0` nos quatro casos nesta configuração: os locadores esperados não chegaram ao top-k textual; isso é uma falha mensurada de recuperação, não evidência fabricada |
| Política | limitada nos quatro casos, com `transfer_not_authorized` e 424 unidades locais não autorizadas para transferência |
| Geração | não houve provider; o caminho determinístico produziu abstinência local, sem citações transferíveis. `TM-ABS-01` foi a única abstinência esperada; `TM-FLOW-01`, `TM-INV-01` e `TM-PROV-01` foram abstinências inesperadas bloqueadas pela política/ausência de pacote |
| Resultado | quatro casos `partial`, com falha primária `retrieval/evidence_recall_below_expected`; a limitação de política permanece registrada separadamente e nenhuma chamada de rede foi tentada |

A política de transferência não foi aberta para fazer os casos passarem. Como
as unidades reais permaneceram locais, o motor não converteu a rubrica em
resposta gerada e não chamou um modelo. O resumo registrou uma abstinência
esperada e correta, e três abstinências efetivas que não eram esperadas pela
rubrica; estas últimas não foram contadas como corretas. O próximo trabalho necessário para
esses casos é melhorar a recuperação/mapeamento dos locadores e definir, em
uma execução explicitamente autorizada, quais evidências podem ser usadas por
um provider; isso não faz parte desta tarefa.

## WSO2 e ERPNext: heterogeneidade e escala

Essas fontes não têm casos de competência conectados ao runner nesta tarefa.
Foram executadas pelo mesmo pipeline de extração real e, portanto, as etapas
de ingestão, recuperação, geração e política aparecem como
`non_applicable`, em vez de serem inferidas a partir de inventário.

| Fonte | Extração limitada | Volume observado | Leitura/concorrência | Limitações |
| --- | --- | --- | --- | --- |
| WSO2 seis CARs | 6 artefatos, 644 contribuições, 38 unidades, 1.054 bytes de conteúdo | 46 estados de cobertura produzidos, 6 incompletos, 13 gaps | 75.115 bytes lidos, concorrência efetiva 4 | limite de evidências; não há semântica profunda de middleware ou runtime |
| ERPNext | 5.316 artefatos, 10.582 contribuições, 5.319 unidades, 1.043.377 bytes de conteúdo | 10.991 estados produzidos, 4.905 incompletos, 4.906 gaps | 142.119.855 bytes lidos, concorrência efetiva 4 | passagem limitada; não comprova inventário completo de uma árvore de aproximadamente 1,9 GiB nem semântica Python/Frappe profunda |

Os digests factuais, registrados sem conteúdo, foram:

| Fonte | Digest factual |
| --- | --- |
| Ticketmaster | `8a31aaa1f9dee2c4b12db96a1361d33c511f41363e2962a5cf1cf3be2e01a16b` |
| WSO2 seis CARs | `7a9c3f5e72092fe1293f9dc3abe60aa845f3e77bb309c653d0439ad7a79c2cae` |
| ERPNext | `34e967ba0dda1a3b710d6658cc001a525efa5d162700a4d0ce384980cf2d35e6` |

Esses números são uma amostra bounded da revisão indicada;
não devem ser usados como contagem definitiva do produto ou como comparação
de desempenho entre máquinas.

## Lacunas e conclusão

- A extração real local está executável para os três recortes, não apenas para
  as fixtures.
- A ingestão canônica/textual local aceita evidência com transferência externa
  negada; a ausência vetorial é explicitamente observada e não gera uma
  chamada externa.
- O Ticketmaster comprova o caminho real até a avaliação, mas revela recall
  textual insuficiente para os locadores atuais. A geração não foi considerada
  aprovada.
- WSO2 e ERPNext comprovam heterogeneidade/escala sob limites, não compreensão
  semântica completa nem `Observed Execution`.
- Não foram executados `live eval`, OpenAI API, rede, banco externo, runtime
  WSO2, site ERPNext ou ambiente Ticketmaster.

Este registro prepara a tarefa 9.5: qualquer baseline futuro deve preservar as
mesmas revisões, limites, política e separação de falhas, ou publicar uma nova
configuração/execução em vez de comparar números incompatíveis.
