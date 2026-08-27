# Configuração da plataforma local

Este documento descreve a configuração tipada de `internal/config`. O
primeiro corte não lê arquivo de configuração: os valores são construídos a
partir dos defaults do pacote e de variáveis de ambiente `MANU_*`. Os comandos
de plataforma que precisam da configuração, como `manu serve`, `manu migrate` e
`manu mcp`, usam esse carregamento; os comandos determinísticos de análise
continuam independentes de banco e IA.

O arquivo [`.env.example`](../.env.example) é apenas uma fotografia segura
dos valores locais mais comuns. Para uma execução local sem Compose, copie-o
para `.env`, ajuste os valores do seu ambiente, revise o conteúdo e exporte as
variáveis em um shell confiável antes de iniciar o comando. O exemplo não
contém senha, chave, token ou DSN com credencial. A aplicação lê as variáveis
do ambiente do processo; ela não interpreta um arquivo `.env` por conta
própria.

Para a célula local com PostgreSQL/pgvector, migração one-shot e API, consulte
[`docs/compose.md`](compose.md).

## Precedência

A precedência normativa é:

```text
overrides/flags > ambiente MANU_* > defaults
```

`config.Load()` começa pelos defaults e lê o ambiente do processo. Os
consumidores que possuírem flags ou overrides tipados devem aplicá-los depois
do carregamento do ambiente e executar `Config.Validate()` novamente. A
função `config.LoadEnv(map[string]string)` existe para testes e para uma
fotografia explícita do ambiente; ela não consulta o ambiente do processo nem
arquivos.

Variáveis `MANU_*` desconhecidas são ignoradas. Uma variável conhecida com
valor inválido faz o carregamento falhar. Durações usam a sintaxe de
`time.ParseDuration`, como `2s`, `500ms` ou `1m`.

## Defaults seguros

Os defaults são finitos e locais. O servidor usa `platform` sem autenticação e
escuta em `127.0.0.1:8080`. A organização padrão é `local`. A política permite
persistência local e nega transferência externa. Embedding, geração e
avaliação real permanecem desabilitados; a avaliação simulada não precisa de
credencial.

Nenhuma senha, DSN, chave de API ou outro segredo possui default. Os campos
`DSN`, `Password` e `APIKey` têm `json:"-"` e também são removidos por
`Config.Redacted()`, `Config.String()` e `slog`.

## Arquivos locais e segredos

O repositório ignora `.env`, `.env.*` (com exceção de `.env.example`),
`*.secret`, `*.secrets`, `/secrets/` e `/config/secrets/`. Isso reduz o risco
de um arquivo de desenvolvimento ser adicionado por engano, mas não substitui
revisão de `git diff`, gerenciamento de segredos ou rotação de credenciais.

Uma instalação pode fornecer os valores por um secret mount ou por outro
mecanismo do ambiente e expô-los ao processo com os nomes `MANU_*_API_KEY` e
`MANU_POSTGRES_PASSWORD`. O primeiro corte não implementa um formato próprio
de secret mount; o requisito é que o segredo permaneça fora do bundle, do
arquivo de exemplo, dos logs, dos diagnósticos e dos documentos versionados.

Exemplo de preparação local:

```bash
cp .env.example .env
# edite .env somente no ambiente local; ele é ignorado pelo Git
# revise o arquivo antes de carregá-lo: o comando abaixo executa atribuições
# do arquivo no shell atual e só deve ser usado com um arquivo confiável
set -a
. ./.env
set +a
manu serve
```

O mesmo carregamento pode preceder `manu migrate`. O `set -a` torna as
atribuições exportadas para o processo filho; o `.` (ou `source`) é uma
operação do shell, não uma capacidade do binário Manu. Não faça source de um
arquivo recebido de terceiros ou alterado sem revisão. O Docker Compose
disponível neste corte lê `.env` por meio de sua própria configuração de
ambiente; isso não significa que `manu serve` ou `manu migrate` interpretem o
arquivo diretamente.

Não use `OPENAI_API_KEY` ou `OPENROUTER_API_KEY` diretamente como uma
configuração implícita do Manu. A chave deve ser injetada no campo da
capacidade que a utilizará (`MANU_EMBEDDING_API_KEY` ou
`MANU_GENERATION_API_KEY`), mantendo embedding e geração independentes.

## Variáveis do servidor e da organização

| Variável | Tipo | Default | Observação |
| --- | --- | --- | --- |
| `MANU_SERVER_MODE` | enum | `platform` | O único modo deste corte. |
| `MANU_SERVER_LISTEN_ADDRESS` | endereço | `127.0.0.1:8080` | Deve ser loopback enquanto não houver autenticação. |
| `MANU_SERVER_READ_TIMEOUT` | duração | `15s` | Prazo de leitura HTTP. |
| `MANU_SERVER_WRITE_TIMEOUT` | duração | `30s` | Prazo de escrita HTTP. |
| `MANU_SERVER_IDLE_TIMEOUT` | duração | `1m` | Prazo de conexão ociosa. |
| `MANU_SERVER_SHUTDOWN_TIMEOUT` | duração | `10s` | Prazo de encerramento. |
| `MANU_SERVER_MAX_HEADER_BYTES` | inteiro | `1048576` | Limite de cabeçalhos. |
| `MANU_SERVER_MAX_BODY_BYTES` | inteiro | `67108864` | Limite de corpo HTTP; deve cobrir `MAX_BUNDLE_BYTES`. |
| `MANU_SERVER_MAX_CONCURRENT_REQUESTS` | inteiro | `64` | Concorrência máxima local. |
| `MANU_ORGANIZATION_ID` | texto | `local` | Identidade da única `Organization` local. |
| `MANU_ORGANIZATION_NAME` | texto | `Local organization` | Nome descritivo; não é uma credencial. |

O endereço deve conter host e porta válidos. São aceitos endereços IPv4/IPv6
de loopback e `localhost`; `0.0.0.0`, `[::]` e endereços remotos são recusados.

## MCP local

| Variável | Tipo | Default | Observação |
| --- | --- | --- | --- |
| `MANU_MCP_ENABLED` | booleano | `false` | Opt-in explícito para `manu mcp`, servidor local por `stdio`; não habilita HTTP, acesso remoto ou autenticação. |

`manu mcp` usa a mesma organização e configuração PostgreSQL de `manu serve` e
`manu migrate`. A migração não é executada automaticamente; aplique-a antes de
iniciar o MCP. A referência operacional do protocolo, cliente, ferramentas,
recursos e limites está em [`mcp.md`](mcp.md).

## Staging durável de ingestão

| Variável | Tipo | Default | Observação |
| --- | --- | --- | --- |
| `MANU_INGESTION_STAGING_DIRECTORY` | caminho absoluto | `/tmp/manu/ingestions` | Diretório privado para o multipart aceito antes do `202` e para retomada após reinício; não é o diretório-fonte do Agent. |

O runtime cria o diretório com permissões restritas e rejeita uma raiz
symlinkada. No Compose, a variável aponta para `/var/lib/manu/ingestions`,
montado no volume nomeado `manu-ingestion-data`.

## Banco PostgreSQL

O tipo se chama `PostgresConfig` porque PostgreSQL é o único banco previsto
para este corte. Campos de conexão podem ser fornecidos por DSN ou por host,
porta, database e usuário. O loader não abre conexão nem verifica a existência
do banco.

| Variável | Tipo | Default | Observação |
| --- | --- | --- | --- |
| `MANU_POSTGRES_DSN` | URL | vazio | Deve usar `postgres://` ou `postgresql://`; pode conter credencial somente no ambiente. |
| `MANU_POSTGRES_HOST` | texto ou diretório de socket Unix | `127.0.0.1` | Obrigatório quando não houver DSN; um caminho absoluto é usado como diretório de socket local. |
| `MANU_POSTGRES_PORT` | inteiro | `5432` | Intervalo `1..65535`. |
| `MANU_POSTGRES_DATABASE` | texto | `manu` | Obrigatório quando não houver DSN. |
| `MANU_POSTGRES_USER` | texto | `manu` | Obrigatório quando não houver DSN. |
| `MANU_POSTGRES_PASSWORD` | segredo | vazio | Nunca aparece em JSON, logs ou defaults. |
| `MANU_POSTGRES_SSL_MODE` | enum | `disable` | `disable`, `allow`, `prefer`, `require`, `verify-ca` ou `verify-full`. |
| `MANU_POSTGRES_MAX_OPEN_CONNS` | inteiro | `10` | Limite do pool. |
| `MANU_POSTGRES_MAX_IDLE_CONNS` | inteiro | `5` | Não pode exceder conexões abertas. |
| `MANU_POSTGRES_CONN_MAX_LIFETIME` | duração | `30m` | Vida máxima de uma conexão. |
| `MANU_POSTGRES_CONN_MAX_IDLE_TIME` | duração | `5m` | Tempo ocioso máximo de uma conexão. |

## Limites e política de conteúdo

| Variável | Tipo | Default |
| --- | --- | ---: |
| `MANU_LIMITS_MAX_BUNDLE_BYTES` | inteiro | `67108864` |
| `MANU_LIMITS_MAX_MANIFEST_BYTES` | inteiro | `1048576` |
| `MANU_LIMITS_MAX_EVIDENCE_UNITS` | inteiro | `10000` |
| `MANU_LIMITS_MAX_EVIDENCE_BYTES` | inteiro | `262144` |
| `MANU_LIMITS_MAX_EVIDENCE_TEXT_BYTES` | inteiro | `65536` |
| `MANU_LIMITS_MAX_QUERY_BYTES` | inteiro | `16384` |
| `MANU_LIMITS_MAX_CONCURRENT_INGESTIONS` | inteiro | `2` |
| `MANU_LIMITS_MAX_CONCURRENT_QUERIES` | inteiro | `16` |
| `MANU_POLICY_PERSIST` | enum | `allow` |
| `MANU_POLICY_EXTERNAL_TRANSFER` | enum | `deny` |

As decisões de persistência e transferência externa são independentes e cada
uma aceita `allow`, `redact` ou `deny`. Os limites de manifesto e texto não
podem exceder seus limites envolventes; o corpo HTTP deve cobrir o bundle.

## Recuperação

| Variável | Tipo | Default |
| --- | --- | ---: |
| `MANU_RETRIEVAL_TOP_K` | inteiro | `20` |
| `MANU_RETRIEVAL_MAX_CANDIDATES` | inteiro | `100` |
| `MANU_RETRIEVAL_MAX_RELATION_HOPS` | inteiro | `1` |
| `MANU_RETRIEVAL_MAX_RELATION_FAN_OUT` | inteiro | `25` |
| `MANU_RETRIEVAL_MAX_PACKAGE_UNITS` | inteiro | `16` |
| `MANU_RETRIEVAL_MAX_PACKAGE_BYTES` | inteiro | `262144` |
| `MANU_RETRIEVAL_MAX_PACKAGE_TOKENS` | inteiro | `16000` |
| `MANU_RETRIEVAL_EXACT_WEIGHT` | número | `1` |
| `MANU_RETRIEVAL_TEXT_WEIGHT` | número | `1` |
| `MANU_RETRIEVAL_VECTOR_WEIGHT` | número | `1` |
| `MANU_RETRIEVAL_RELATION_WEIGHT` | número | `1` |

Os pesos devem ser finitos e não negativos, pelo menos um sinal deve estar
ativo, e a expansão relacional não ultrapassa um salto. Os limites do pacote
devem permanecer dentro dos limites do bundle e das unidades de evidência.

## Embedding e geração

Cada capacidade possui provedor, modelo, endpoint, prazo e orçamento
independentes. O valor `enabled` padrão é `false`.

| Variável | Tipo | Default |
| --- | --- | --- |
| `MANU_EMBEDDING_ENABLED` | booleano | `false` |
| `MANU_EMBEDDING_PROVIDER` | enum | vazio |
| `MANU_EMBEDDING_MODEL` | texto | vazio |
| `MANU_EMBEDDING_BASE_URL` | URL HTTP(S) | vazio |
| `MANU_EMBEDDING_API_KEY` | segredo | vazio |
| `MANU_EMBEDDING_TIMEOUT` | duração | `30s` |
| `MANU_EMBEDDING_MAX_BATCH_SIZE` | inteiro | `32` |
| `MANU_EMBEDDING_DIMENSION` | inteiro | `0` |
| `MANU_GENERATION_ENABLED` | booleano | `false` |
| `MANU_GENERATION_PROVIDER` | enum | vazio |
| `MANU_GENERATION_MODEL` | texto | vazio |
| `MANU_GENERATION_BASE_URL` | URL HTTP(S) | vazio |
| `MANU_GENERATION_API_KEY` | segredo | vazio |
| `MANU_GENERATION_PROTOCOL` | enum | `responses` |
| `MANU_GENERATION_TIMEOUT` | duração | `1m` |
| `MANU_GENERATION_MAX_OUTPUT_TOKENS` | inteiro | `2048` |
| `MANU_GENERATION_TEMPERATURE` | número | `0` |

Na célula Compose, todas essas variáveis de embedding e geração são
encaminhadas explicitamente ao serviço `api`; não basta alterar o `.env` se
uma variável não estiver na lista do serviço. Chaves continuam sem default e
devem ser injetadas apenas no ambiente local ou por um mecanismo de segredo.
Os defaults do Compose conservam `*_ENABLED=false`, orçamento zerado e
`MANU_POLICY_EXTERNAL_TRANSFER=deny`, portanto uma configuração incompleta
falha fechada ou permanece sem chamadas externas.

O carregador de configuração reconhece `openai`, `openrouter`,
`openai-compatible` e `simulated`. Uma capacidade habilitada exige provedor,
modelo, prazo e limites positivos. `openai`, `openrouter` e
`openai-compatible` exigem chave fornecida externamente; `openrouter` e
`openai-compatible` também exigem `BASE_URL` sem credencial embutida.
`simulated` não faz rede nem exige chave. O protocolo de geração deve ser
`responses` ou `chat_completions`. No runtime deste corte, o adaptador
compatível só implementa o dialeto OpenRouter; selecionar o provider genérico
`openai-compatible` é rejeitado até que outro dialeto seja especificado e
implementado em uma mudança própria.

### Perfis independentes

Embedding e geração possuem perfis separados. Cada um tem seu próprio
provedor, modelo, endpoint, prazo, limites e orçamento:

- trocar somente o modelo/provedor de geração não exige reindexar os vetores;
- trocar o perfil semântico de embedding cria uma projeção incompatível e
  exige o rebuild de embeddings definido no ADR 0003;
- vetores de perfis diferentes não podem participar da mesma ordenação;
- não há fallback silencioso entre os protocolos declarados.

O valor `Provider` identifica a integração escolhida, não uma garantia de que
dois provedores tenham exatamente as mesmas capacidades. As combinações
suportadas neste corte são:

| Provedor | Configuração de geração | Endpoint e chave | Observação |
| --- | --- | --- | --- |
| `openai` | `MANU_GENERATION_PROTOCOL=responses` | A chave fica em `MANU_GENERATION_API_KEY`; o endpoint padrão do adaptador é usado quando `BASE_URL` está vazio. | Adaptador nativo da API OpenAI. |
| `openrouter` | `MANU_GENERATION_PROTOCOL=chat_completions` | Informe `MANU_GENERATION_BASE_URL` explicitamente e injete a chave em `MANU_GENERATION_API_KEY`. | Integração compatível validada inicialmente com OpenRouter; não implica equivalência silenciosa. |
| `openai-compatible` | `MANU_GENERATION_PROTOCOL=chat_completions` | Reconhecido pelo contrato de configuração, mas rejeitado pelo runtime atual até haver um dialeto explícito. | Não tratar endpoints arbitrários como equivalentes ao OpenRouter. |
| `simulated` | `responses` ou `chat_completions` | Não usa endpoint nem chave. | Execução determinística e sem rede. |

A mesma seleção vale para as variáveis com prefixo `MANU_EMBEDDING_*`, com o
modelo e a dimensão correspondentes ao perfil de embedding; no runtime atual,
o dialeto compatível operacional também é somente OpenRouter. Não existe uma
chave global compartilhada automaticamente entre as duas capacidades.

Para OpenAI, use `MANU_*_API_KEY` apenas na capacidade que será chamada. Para
OpenRouter, além da chave externa, configure o `BASE_URL` da capacidade e o
protocolo de geração compatível. O valor efetivamente usado pelo adaptador,
incluindo o modelo retornado pelo provedor, deve ser registrado em metadados
de auditoria sem registrar a credencial.

## Relação com o fluxo local

`manu analyze` é um comando determinístico do Agent. O formato `legacy`
(`v1alpha1`) continua sendo o padrão; para a plataforma, produza diretamente o
bundle estendido com `--output-mode bundle --organization-id <id>` e envie-o
com `manu ingest --bundle <diretório>`. O modo bundle grava manifesto,
artefatos, contribuições e evidências disponíveis, sem incluir a raiz local da
fonte. A plataforma recebe o bundle por HTTP, persiste os fatos no PostgreSQL
e deriva projeções antes de atender consultas.

O `manu serve` grava o multipart em staging privado, publica-o atomicamente
com um marcador `.ready` e cria o job antes do `202`; o executor recupera jobs
pendentes após reinício usando o volume de staging. A execução desse fluxo,
incluindo readiness e persistência, está no
[registro de verificação 10.3](verification/10-3-local-cell.md).

Não existe comando CLI público para reconstruir projeções. A troca de perfil de
embedding exige reconstrução explícita a partir das unidades canônicas e não
permite misturar vetores de perfis; o volume não deve ser apagado como atalho.
O ciclo de curadoria editorial não está implementado. Resultados derivados de
análise são conhecimento observado quando sustentados por artefato e método;
respostas de IA são conhecimento gerado, sempre dependente de um pacote de
evidências limitado. A política de transferência externa continua independente
da autorização para ler a fonte.

## Orçamento

Os quatro campos abaixo se repetem nos prefixos
`EMBEDDING_BUDGET`, `GENERATION_BUDGET` e `EVALUATION_BUDGET`:

| Sufixo | Tipo | Default |
| --- | --- | ---: |
| `MAX_REQUESTS` | inteiro | `0` (não autorizado) |
| `MAX_INPUT_TOKENS` | inteiro | `0` (não autorizado) |
| `MAX_OUTPUT_TOKENS` | inteiro | `0` (não autorizado) |
| `MAX_COST_USD` | número | `0` (não autorizado) |

Exemplos de nomes completos são
`MANU_EMBEDDING_BUDGET_MAX_REQUESTS`,
`MANU_GENERATION_BUDGET_MAX_OUTPUT_TOKENS` e
`MANU_EVALUATION_BUDGET_MAX_COST_USD`. Todos os valores devem ser finitos e
não negativos. Zero significa que a dimensão não está autorizada. Uma
capacidade externa habilitada e `MANU_EVALUATION_LIVE=true` exigem valores
positivos explícitos para requests, tokens de entrada, tokens de saída e
custo; não há limite financeiro implícito.

`MANU_EVALUATION_LIVE` (booleano, default `false`) mantém a avaliação real
opt-in. A avaliação padrão é simulada e determinística.

Uma execução totalmente simulada deve manter `MANU_EVALUATION_LIVE=false`, a
política `MANU_POLICY_EXTERNAL_TRANSFER=deny` e as capacidades externas
desabilitadas (`MANU_EMBEDDING_ENABLED=false` e
`MANU_GENERATION_ENABLED=false`). Quando o runner simulado estiver sendo
usado, seus provedores determinísticos não abrem conexão, não exigem chave e
não geram custo externo. A configuração simulada não representa a qualidade
ou a capacidade de um provedor real.

Para habilitar uma avaliação real, todas as condições precisam ser explícitas:

1. a política de transferência deve permitir o conteúdo aplicável;
2. a capacidade correspondente deve estar habilitada com provedor, modelo,
   chave externa e protocolo válidos;
3. cada orçamento de requests, tokens de entrada, tokens de saída e custo deve
   ser positivo;
4. o resultado deve registrar modelo, uso, latência, custo e hashes/IDs do
   conteúdo transferido, sem registrar a chave nem o conteúdo integral.

O modo real é opt-in e não é necessário para iniciar a célula local, executar
ingestão, recuperar sinais não vetoriais ou executar testes sem rede.

## Validação e segurança operacional

`Config.Validate()` é local e determinístico: não abre socket, não conecta ao
PostgreSQL, não resolve endpoint e não chama provedor de IA. Ele recusa:

- servidor fora de loopback no modo sem autenticação;
- organização sem identificador;
- banco sem campos mínimos, porta/pool inválidos ou DSN que não seja
  PostgreSQL;
- limites positivos inconsistentes e pesos de recuperação inválidos;
- capacidades habilitadas sem provedor, modelo, credencial externa, endpoint
  explícito quando necessário ou orçamento/limites coerentes;
- orçamento negativo, `NaN` ou infinito, ou operação externa/live sem limites
  positivos explícitos.

Não há suporte a arquivo de configuração nesta etapa. Um secret mount pode
popular as mesmas variáveis no ambiente do processo; seu conteúdo não deve
ser colocado em fixtures, manifests, logs, JSON ou documentação.
