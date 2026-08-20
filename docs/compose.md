# Célula local com Docker Compose

O arquivo [`compose.yaml`](../compose.yaml) descreve a célula local inicial do
Manu. Ela possui somente três serviços:

- `postgres`: PostgreSQL 18 com pgvector `0.8.6`, sem a porta 5432 publicada no
  host e com um volume de socket Unix compartilhado com a API;
- `migrate`: a imagem Manu executando `manu migrate` uma única vez;
- `api`: a mesma imagem Manu executando `manu serve` com a rede host Linux,
  publicada somente pelo bind obrigatório em `127.0.0.1`.

O servidor sem autenticação mantém o bind obrigatório em
`127.0.0.1:8080` (ou no endereço de loopback completo fornecido por
`MANU_SERVER_LISTEN_ADDRESS`). A rede host é usada nesta célula Linux para
que o bind de loopback seja realmente acessível pelo cliente no host; não há
um proxy que relaxe essa regra. A porta interna `5432` do PostgreSQL não
aparece em `ports`; `migrate` acessa o banco pelo nome do serviço e a API usa
o diretório de socket Unix `/var/run/postgresql`, montado nos dois containers.

O banco usa o volume nomeado `manu-postgres-data`, e a API depende da conclusão
bem-sucedida de `migrate`. O healthcheck do banco usa `pg_isready`. A API
mantém o staging de bundles no volume nomeado `manu-ingestion-data`, separado
do diretório temporário do Agent. A configuração tipada
`MANU_INGESTION_STAGING_DIRECTORY` aponta para esse volume. Como a imagem final do Manu é `scratch` e não
contém shell ou cliente HTTP, o comando `manu ready` faz a sondagem HTTP de
`/readyz` com o próprio binário. A API só fica saudável depois que PostgreSQL e
o catálogo de migrações compatível estão prontos; `/healthz` continua sendo o
contrato de liveness.

Depois de `migrate`, o fluxo é o Agent produzir um Analysis Bundle estendido,
o cliente transmiti-lo para a API, o processo gravar o multipart no staging
durável e criar o job PostgreSQL antes do `202`. O executor limitado do mesmo
processo recupera o bundle pelo digest de identidade após reinício e persiste
fatos antes das projeções e consultas. O comando público `manu analyze` mantém
o resultado legado `v1alpha1` como padrão, mas `--output-mode bundle
--organization-id <id>` produz diretamente o Analysis Bundle estendido que
`manu ingest` transmite; não há uma conversão separada a executar nesta célula.

## Preparação e validação

O Compose pode ler `.env` para interpolar os valores do arquivo. O `.env` é
local e ignorado pelo Git; o exemplo versionado não contém credenciais:

```bash
cp .env.example .env
docker compose config --quiet
docker compose up --build
```

O serviço `api` encaminha para o processo `manu serve` os perfis completos e
independentes de embedding e geração: provedor, modelo, `BASE_URL`, chave,
protocolo, prazos, dimensão/tamanho de lote, temperatura, limite de saída e
os quatro limites de orçamento. O mesmo vale para `MANU_POLICY_PERSIST` e
`MANU_POLICY_EXTERNAL_TRANSFER`. Os defaults mantêm ambas as capacidades
desabilitadas, orçamento zerado e transferência externa negada; portanto, a
configuração da imagem não faz chamadas de IA por acidente.

Para habilitar uma capacidade, defina os campos correspondentes no `.env`
ignorado, forneça uma chave somente no ambiente local ou em um mecanismo de
segredo e use um orçamento positivo completo. Por exemplo, a combinação
OpenAI usa `MANU_*_PROVIDER=openai` e o protocolo de geração
`MANU_GENERATION_PROTOCOL=responses`; a combinação OpenRouter usa
`MANU_*_PROVIDER=openrouter`, `BASE_URL` explícito e
`MANU_GENERATION_PROTOCOL=chat_completions`. Não há uma chave global nem
fallback entre os protocolos. Para os testes locais, mantenha os provedores
vazios, `*_ENABLED=false` e os orçamentos em zero, como no
[`.env.example`](../.env.example).

Com a API pronta, o fluxo de uma fonte local é:

```bash
manu analyze --root /caminho/para/fonte \
  --output /caminho/para/analysis-bundle \
  --output-mode bundle --organization-id local --source-id cliente
manu ingest --bundle /caminho/para/analysis-bundle
manu ingestion <ingestion-uuid>
manu ask --kind inventory --question 'quais artefatos existem?'
```

Use `manu ready` para sondar a prontidão da API. O identificador retornado por
`manu ingest` deve ser consultado até um estado terminal; ingestão e consulta
continuam limitadas ao loopback e à organização configurada no processo.

A etapa `migrate` não deve ser iniciada novamente enquanto já houver uma
execução em andamento. Para verificar os estados sem iniciar os serviços:

```bash
docker compose config
docker compose ps
docker compose logs migrate
```

O comando `up` não é necessário para a validação estrutural. O Compose não faz
pull ou build durante `docker compose config`; o primeiro `up --build` pode
precisar baixar a imagem `pgvector/pgvector:0.8.6-pg18-bookworm` e as
dependências do build.

## Autenticação local do banco

Para que a configuração de exemplo funcione sem uma senha versionada, a
célula local usa `POSTGRES_HOST_AUTH_METHOD=trust` por padrão. Esse modo é
deliberadamente limitado ao ambiente local: o banco não tem porta publicada,
mas a rede Docker deve ser considerada parte do perímetro local. Uma
instalação que exigir autenticação deve fornecer, fora do Git, uma senha em
`MANU_POSTGRES_PASSWORD` e substituir `POSTGRES_HOST_AUTH_METHOD` por um método
compatível com sua política antes de iniciar uma nova instância do volume.

A troca de credenciais ou método de autenticação não é uma migração automática
do Manu. Preserve a política operacional do ambiente e não coloque o valor no
`compose.yaml`, no `.env.example`, em logs ou em bundles.

## Limites deste Compose

Esta é uma célula local de desenvolvimento e verificação. As versões estão
fixadas por tag (`pgvector/pgvector:0.8.6-pg18-bookworm` e Go `1.26.6` no
Dockerfile); um digest por arquitetura ainda depende de uma verificação de
imagem no ambiente e não foi inventado neste arquivo. O Compose não adiciona
MinIO, Redis, fila, worker separado ou provedor de IA. Embeddings, geração e
avaliação real permanecem desabilitados pelos defaults seguros.

O serviço de API só pode ser exposto em loopback enquanto não houver
autenticação. O healthcheck de `api` verifica o endpoint real `/readyz`, que
consulta a conexão e o catálogo de migrações; `/healthz` permanece separado
para liveness. A verificação operacional completa de build multiarch,
reinício, persistência, ingestão e consulta está registrada em
`docs/verification/10-3-local-cell.md` quando executada no ambiente.

Não existe no Compose uma fila, worker separado ou comando de reconstrução de
projeções. PostgreSQL é a fonte canônica; trocar o perfil de embedding exige
uma operação de rebuild explícita e não autoriza apagar o volume. A célula não
oferece autenticação, UI, SaaS compartilhado ou exposição remota.
