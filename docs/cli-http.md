# Clientes CLI da API local

O servidor local é iniciado com `manu serve` e permanece limitado ao endereço
de loopback configurado. Enquanto não houver autenticação, os clientes
aceitam somente URLs HTTP de loopback com porta explícita; o padrão é
`http://127.0.0.1:8080`. O contrato detalhado está em
[`openapi.json`](openapi.json).

O servidor fixa a `Organization` configurada no processo. O corpo de uma
consulta não pode escolher outra organização e os endpoints não têm
autenticação nesta etapa. Não publique a porta em uma rede não confiável e não
trate essa célula como um serviço SaaS ou de produção.

## Fluxo mínimo

Em um terminal, com a configuração do PostgreSQL e da organização disponível:

```bash
manu migrate
manu serve
```

Em outro terminal, o Agent produz o Analysis Bundle estendido e o cliente o
envia sem carregar o bundle inteiro em memória:

```bash
manu analyze --root /caminho/para/fonte \
  --output /caminho/para/analysis-bundle \
  --output-mode bundle --organization-id local --source-id cliente
manu ingest --bundle /caminho/para/analysis-bundle
manu ingestion <ingestion-uuid>
manu ask --kind inventory --question 'quais artefatos existem?'
```

O modo `bundle` do `manu analyze` exige `--organization-id` e grava o manifesto,
artefatos, contribuições e evidências disponíveis para ingestão; a raiz local
da fonte não entra no envelope portátil. O modo `legacy` (`v1alpha1`) continua
sendo o padrão para compatibilidade. Não é necessário nem existe uma
subcomanda separada de conversão: para o caminho da plataforma, gere o bundle
diretamente com `--output-mode bundle`.

O `manu serve` mantém o executor no mesmo processo. O multipart é gravado em
staging privado, recebe um marcador `.ready` somente após a publicação atômica
e cria o job durável antes do `202`; jobs pendentes podem ser recuperados após
reinício usando o volume de staging. O fluxo completo, incluindo migração,
readiness, persistência e as limitações da célula Linux-first, está no
[registro de verificação 10.3](verification/10-3-local-cell.md).

O identificador de ingestão vem na resposta de `manu ingest`. O comando pode
usar `--format json` (ou `--json`) para automação. A saída JSON contém somente
o envelope da API; conteúdo bruto de requisição, segredos e diagnósticos de
transporte não são impressos.

## Consulta e evidência

Toda consulta precisa declarar o tipo de conhecimento solicitado. O CLI não
infere esse tipo a partir da pergunta:

```bash
manu ask --kind inventory --question 'quais artefatos existem?'
manu ask --kind possible_flow 'qual fluxo é possível neste código?'
manu ask --kind observed_execution --question 'qual execução foi observada?'
manu ask --kind business_intent --question 'qual intenção de negócio está documentada?'
manu evidence <evidence-uuid>
```

`--source-id` e `--snapshot-id` podem restringir a consulta; o snapshot só é
aceito quando a fonte também é informada. Os comandos `ask` e `evidence`
também aceitam `--server`, `--timeout`, `--format` e `--json`.

Uma resposta `partial` ou `abstained` é uma resposta válida com lacunas e
retorna código de saída `3`. Uma resposta concluída retorna `0`. Erros
técnicos, indisponibilidade, resposta HTTP inválida ou estado `failed` retornam
`1`; argumentos inválidos, URL fora de loopback ou bundle inexistente retornam
`2`. Cancelamento e timeout não imprimem o conteúdo da pergunta ou do bundle.

Os clientes fazem requisições HTTP síncronas para consultas e limitam o
conteúdo de respostas. Redirecionamentos são recusados: um servidor local que
responda `3xx` não recebe uma segunda requisição nem pode encaminhar o bundle
para outro host.

## Superfície HTTP

| Método e caminho | Uso | Resultado normal |
| --- | --- | --- |
| `GET /healthz` | Liveness do processo, sem consultar IA ou PostgreSQL | `200` |
| `GET /readyz` | Compatibilidade do schema e dependências locais | `200` ou `503` |
| `POST /api/v1/ingestions` | Receber `multipart/form-data` do bundle | `202` com `id` e estado |
| `GET /api/v1/ingestions/{id}` | Consultar job da organização fixa | `200` |
| `POST /api/v1/queries` | Executar consulta síncrona | `200` ou `problem+json` |
| `GET /api/v1/queries/{id}` | Reinspecionar consulta persistida | `200` ou `problem+json` |
| `GET /api/v1/evidence/{id}` | Inspecionar uma unidade autorizada | `200` ou `problem+json` |

Ingestão é assíncrona e expõe `pending`, `running`, `completed`, `partial` ou
`failed`. Consulta pode retornar `completed`, `partial`, `abstained` ou
`failed`; `partial` e `abstained` são resultados válidos com limitação, não
prova de execução observada. Erros de transporte e validação usam
`application/problem+json`, código estável e `X-Request-ID`, sem ecoar a
pergunta, o bundle, credenciais ou diagnóstico interno.

O request de ingestão é limitado por corpo, manifesto, bytes/unidades de
evidência e concorrência. A consulta aceita JSON, exige `kind` explícito e tem
limite de 16 KiB por request no default. Os valores completos e suas variáveis
estão em [`configuration.md`](configuration.md); não aumente os limites para
contornar a ausência de um parser ou de uma projeção.

## Conhecimento e evidência

O conteúdo recebido do bundle é dado não confiável: a API valida identidade,
digest, referências, limites e decisões de transferência antes da persistência
ou de uma chamada externa. **Conhecimento observado** é uma observação
proveniente do artefato e do método do analisador; **conhecimento gerado** é a
resposta do gateway sobre um pacote limitado, com citações; **conhecimento
curado** seria uma contribuição humana revisável, mas o ciclo editorial ainda
não está persistido neste corte. Sem evidência transferível, a consulta deve
abster-se; ela não pode promover texto do modelo a evidência da organização.

Não há comando CLI público para reconstruir projeções. PostgreSQL conserva os
fatos canônicos, enquanto projeções textual, relacional e vetorial podem ser
recriadas por uma operação interna/operacional futura. Alterar o perfil de
embedding exige rebuild explícito e não permite misturar vetores de perfis.
