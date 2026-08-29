# Avaliação de eficiência de contexto — tarefa 8.8

Este registro documenta a execução da linha de base `direct-source` e da
variante `manu-context` em 2026-08-29. A execução ocorreu localmente em Linux,
na célula Docker Compose, com a imagem construída a partir do commit
`07d794b` e o schema 6. O registro é uma observação deste recorte; não é SLA,
garantia geral ou promessa de economia.

## Reprodução e identidade

O ambiente foi iniciado na raiz do repositório com:

`docker compose config --quiet` e `docker compose up -d --build`.

Foram avaliados 9 casos e 27 execuções sobre os casos versionados
[`context-efficiency.v1alpha2.json`](../../testdata/evaluation/context-efficiency.v1alpha2.json).
As variantes foram `direct-source` (`direct`), `manu-context` (`manu`) e
`text-retrieval` (`text`). O contexto de execução usou `manu-context`
`runtime-v1`, sem modelo ou `Generator`; a política manteve
`external_transfer=deny`. Essa política bloqueou transferência externa, mas o
`Context Package` local permaneceu utilizável.

Os artefatos auditáveis são o [registro bruto](context-efficiency-8-8.raw.json)
e o [resumo](context-efficiency-8-8.summary.json). Seus digests são:

| Artefato | Digest SHA-256 |
| --- | --- |
| bruto | `095b4dccd8a1a82d828342532898ad88b3cc4f692f6d5e60cbc7694a3404399c` |
| artefato bruto | `095b4dccd8a1a82d828342532898ad88b3cc4f692f6d5e60cbc7694a3404399c` |
| resumo | `fa28830b2a60bfc5458dcb9a0dd592e8ae04963173bc575e8fbb1e78299ab35a` |
| conjunto de casos | `0b02b99cc37e0d82a9da0b5facae3809844dbd642012b70253b8fef11671bbb7` |
| entrada | `20c9dca9dedc21485e1a918cc82c855d956d9dfc5636883a583099cf7aef6b8a` |
| resultado | `4552e5b65cf4a4425345fef9f4ccb71c764be511de4ed3381f4f6ac52593c417` |
| reprodutibilidade | `b8111ec55f10909537477600a12a003ee9af50935c45a925c77a4717ebcebf82` |

Uma repetição em `/tmp` produziu resumo semanticamente idêntico. Somente as
durações e os digests dependentes mudaram; portanto, o resultado não é
reproduzível byte a byte.

## Resultado observado

| Variante | Execuções | Resultado | Recall de evidência | Precisão de evidência | Bytes lidos | Chamadas de ferramenta |
| --- | ---: | --- | --- | --- | ---: | ---: |
| `direct-source` | 9 | 9 `limited` | média `1` (mín./mediana/máx. `1/1/1`) | média `1` (mín./mediana/máx. `1/1/1`) | média `960` (mín./mediana/máx. `782/806/1292`) | média `1.6667` |
| `manu-context` | 9 | 9 `limited` | média `0.222222` (mín./mediana/máx. `0/0/1`) | disponível em 3 casos; média `1` | média `20226.5556` (mín./mediana/máx. `0/26421/34343`) | média `1` |
| `text-retrieval` | 9 | 9 `unavailable` | indisponível | indisponível | indisponível | indisponível |

Nenhuma das 27 execuções foi `successful` ou `completed`. Houve
correspondência parcial de evidências na variante Manu em Java impacto
(`2/2`), Python impacto (`1/2`) e Python localização (`1/2`). Isso não foi
contado como sucesso: os resultados permaneceram content-free, sem geração,
claims concluídos ou citações válidas.

As 18 comparações ficaram não comparáveis (`comparable=0`), pois não houve
resultado correto, completo e sustentado em ambas as variantes. Assim,
`savings=null`: não há alegação de economia, nem cálculo de custo por sucesso.

## Limitações

- Não houve execução de modelo, `Generator`, rede ou transferência externa;
  tokens e custo permaneceram indisponíveis.
- A variante textual ficou indisponível. Os casos de impacto usaram fallback
  pela pergunta, sem alvo tipado.
- Três pacotes Manu tiveram zero bytes: Java explicação, Java localização e
  WSO2 impacto.
- Na comparação WSO2, a diferença entre locador por linha e por byte foi
  tratada conservadoramente como não correspondente.
- O resultado ficou sujeito ao teto de observações e aos termos das perguntas;
  essas condições, além do conteúdo livre, impedem generalização para outras
  fontes, tarefas ou organizações.
