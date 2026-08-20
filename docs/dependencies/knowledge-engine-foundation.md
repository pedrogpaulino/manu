# Fundação de dependências do Knowledge Engine

## Escopo e status

Este documento registra a pesquisa da tarefa 1.1 da mudança
`define-first-evidence-backed-query-slice`. É uma recomendação técnica para
o recorte. A recomendação de `pgx/v5` e do módulo raiz `pgvector-go` já foi
aplicada no `go.mod`; o adaptador `pgvector-go/pgx` continua fora do corte. O
retrato de versões e fontes foi verificado em 18 de agosto de 2026.

O recorte segue as restrições já definidas: módulo Go único, `net/http`, SQL
explícito e parametrizado, migrações próprias embarcadas e nenhuma ORM,
framework de HTTP ou SDK de provedor. PostgreSQL é a fonte de verdade; a
projeção vetorial é derivada e reconstruível.

### Classificação da evidência

- **Observado:** versões, datas de release, suporte, licenças, advisories e
  tamanhos publicados nas fontes oficiais listadas ao final.
- **Curado:** as restrições de arquitetura e domínio já aprovadas nos artefatos
  da mudança e na documentação do projeto.
- **Gerado:** a comparação de footprint, a seleção recomendada e os controles
  derivados da combinação entre as fontes observadas e o design.
- **Hipótese/opção futura:** uso de `database/sql`, índices aproximados e SDKs
  de provedor; nenhum deles é necessário ou aprovado neste corte.

## Recomendação executável

| Componente | Versão/tag recomendada | Suporte observado na data do recorte | Licença | Motivo e condição |
| --- | --- | --- | --- | --- |
| PostgreSQL | **18.6** | Major atual com suporte até 14 nov. 2030; 18.6 é o minor corrente | PostgreSQL License, permissiva | Alinha o recorte ao major mais novo suportado. Fixar o minor e atualizar apenas após validação. |
| Extensão pgvector | **0.8.6** | Release estável atual; suporta PostgreSQL 13+ | PostgreSQL/University of California, permissiva | Inclui correções recentes de memória e índices. Não usar `0.8.7`, que ainda está listado como unreleased. |
| Imagem do banco | `pgvector/pgvector:0.8.6-pg18-bookworm` | Tag publicada para PostgreSQL 18 | Licenças do PostgreSQL, pgvector e imagem base | Menor que a variante `trixie` no catálogo oficial consultado. A implementação deve fixar também o digest da arquitetura alvo. |
| Driver PostgreSQL | `github.com/jackc/pgx/v5 v5.10.0` | Release estável mais recente; exige Go 1.25+ e suporta PostgreSQL 14+ | MIT | Driver/toolkit puro Go, nativo de PostgreSQL e adequado ao SQL explícito e a pools. Usar o protocolo estendido com parâmetros. |
| Tipos pgvector — núcleo | `github.com/pgvector/pgvector-go v0.4.1` | Último release tagueado; a linha v0 ainda não é v1 estável; exige Go 1.25+ | MIT no repositório upstream | Dependência direta já adotada no `go.mod`; fornece tipos e conversão de vetor. |
| Tipos pgvector — adaptador pgx | `github.com/pgvector/pgvector-go/pgx v0.4.1` | Último release tagueado, publicado em jul. 2026 | O repositório upstream publica MIT, mas o pkg.go.dev informa “None detected” para este submódulo | Opção não adotada neste corte. Pode registrar codecs nativos no pgx, **condicionada à confirmação da licença no artefato/tag antes de qualquer adoção**. Acrescenta `github.com/x448/float16 v0.8.4` ao grafo. |

Neste corte, `github.com/jackc/pgx/v5 v5.10.0` e
`github.com/pgvector/pgvector-go v0.4.1` já são dependências diretas e usadas
pela implementação. O adaptador `pgvector-go/pgx` e o `float16` transitivo
correspondente não foram adicionados; qualquer mudança nessa decisão exige
nova confirmação de licença, grafo e testes.

A implementação atual usa a tag `pgvector/pgvector:0.8.6-pg18-bookworm` no
Compose. Essa tag fixa o release do pgvector e o major do PostgreSQL, mas não
fixa o minor 18.6 nem um digest por arquitetura; portanto, a recomendação de
18.6 acima não deve ser lida como a garantia de um pin de produção já
entregue. Essa fixação continua uma pendência operacional explícita.

## PostgreSQL e pgvector

### PostgreSQL 18.6

A [política oficial de versionamento do PostgreSQL][pg-versioning], atualizada
em 13 de agosto de 2026, lista 18.6 como o minor corrente e o PostgreSQL 18
como suportado até 14 de novembro de 2030. A mesma tabela lista 17.11, 16.15,
15.19 e 14.24 como majors ainda suportados; 13 e anteriores já não são
suportados. O [anúncio oficial de releases][pg-release] informa que a rodada
de 13 de agosto trouxe correções de segurança e mais de 110 correções de bugs.
Por isso, 18.6 é a escolha deste corte; PostgreSQL 19 beta não é uma base de
produção.

A [licença oficial][pg-license] é a PostgreSQL License, permissiva e adequada
à distribuição do produto. A documentação de [plataformas suportadas][pg-platforms]
confirma Linux e os principais sistemas usados no desenvolvimento; a
documentação de [requisitos de instalação][pg-install] mostra que uma
compilação própria exige ferramentas de C/build. Para reduzir o footprint
operacional e manter builds reproduzíveis, o recorte deve usar a imagem do
pgvector em vez de compilar o servidor durante cada build da aplicação.

### pgvector 0.8.6

O [README oficial do pgvector][pgvector-readme] declara suporte a PostgreSQL
13+ e a [changelog oficial][pgvector-changelog] marca 0.8.6 como o release de
29 de julho de 2026. Essa versão corrige, entre outros itens, overflow de
buffer do índice IVFFlat em sistemas de 32 bits e problemas de memória em
IVFFlat; 0.8.7 está apenas como unreleased. O [arquivo de licença do
projeto][pgvector-license] usa a licença PostgreSQL/University of California.

O recorte começa com busca exata, que é a referência de recall do próprio
pgvector, e só deriva índices aproximados quando a mudança especificar isso.
Essa escolha evita transformar uma otimização de busca em fonte de verdade.
O [catálogo oficial da imagem][pgvector-tags] informa, para a tag
`0.8.6-pg18-bookworm`, aproximadamente 151,43 MB comprimidos em amd64 e
149,45 MB em arm64; `0.8.6-pg18-trixie` é maior (aproximadamente 156,36 MB e
154,97 MB). São tamanhos de download do manifesto consultado, não uma
promessa de tamanho descomprimido. A imagem e o digest devem ser verificados
para cada arquitetura no trabalho de infraestrutura.

O [security policy do pgvector][pgvector-security] orienta o reporte por
canal próprio. Antes de publicar a imagem, a aplicação deverá repetir a
verificação da changelog e a varredura da imagem; nenhum aviso de segurança é
considerado permanentemente resolvido apenas por uma versão escrita neste
documento.

## Dependências Go adotadas e opções não adotadas

O repositório já fixa `go 1.25` e `toolchain go1.26.6`. A [release oficial do
Go 1.26.6][go-release], publicada em 13 de agosto de 2026, contém correções de
segurança no toolchain e na biblioteca padrão; ela é a base recomendada para
compilar estes módulos. As dependências adotadas exigem no máximo Go 1.25,
portanto não exigem elevar o toolchain.

### `pgx/v5` 5.10.0

O [README da release 5.10.0][pgx-readme] descreve o pgx como driver/toolkit
puro Go, com suporte a recursos específicos do PostgreSQL, pool, COPY,
transações, TLS e tipos customizados. Ele recomenda a interface nativa quando
a aplicação é exclusivamente PostgreSQL, exatamente o caso do Knowledge
Engine; o adaptador `database/sql` continua disponível se uma dependência
futura exigir essa interface. A release 5.10.0, de 3 de junho de 2026, é a
última tag estável consultada.

O [go.mod da release][pgx-go-mod] requer Go 1.25 e declara, para o caminho do
driver, `pgpassfile`, `pgservicefile`, `puddle/v2`, `x/sync` e `x/text`. O mesmo
arquivo declara pacotes auxiliares de teste; eles não são necessários para o
binário de produção. O pacote tem 20 imports no [catálogo do Go][pgx-pkg], uma
medida mais útil para este corte do que contar linhas do repositório. A
[licença upstream][pgx-license] é MIT.

Há histórico de advisories corrigidos em versões anteriores: [GO-2026-4771]
abrange versões anteriores ao endurecimento de decodificação da série 5.9;
[GO-2024-2567] trata um panic no Pipeline anterior a 5.5.2; e
[GO-2024-2606] trata o CVE-2024-27304 anterior a 5.5.4. A changelog de 5.10.0
também registra limites para decodificadores binários, limite de iterações
SCRAM e outras defesas contra servidor PostgreSQL malicioso ou comprometido.
Assim, 5.10.0 é a versão adotada neste recorte; ainda é necessário executar
`govulncheck ./...` quando essa ferramenta estiver disponível no ambiente.

O driver deve usar consultas parametrizadas e o protocolo estendido. Não se
deve habilitar `QueryExecModeSimpleProtocol` para valores controlados por
usuário: a [correção de SQL injection da changelog 5.9.2][pgx-changelog]
mostra por que o modo simples não é um substituto para parâmetros.

### `pgvector-go` 0.4.1 e adaptador pgx

O [repositório oficial][pgvector-go] fornece tipos de vetor para Go e
adaptadores para pgx. O [go.mod da release 0.4.1][pgvector-go-mod] contém
somente o módulo raiz e `go 1.25.0`; a [changelog da release][pgvector-go-changelog]
marca 0.4.1 em 29 de julho de 2026 e registra correções para possíveis panics
em `Parse` e `DecodeBinary`. O [LICENSE do release][pgvector-go-license] é MIT.

O pacote `github.com/pgvector/pgvector-go/pgx` é a opção adequada para
registrar os codecs no driver nativo. No catálogo do Go, a versão 0.4.1 tem 9
imports e 16 importadores; seu `go.mod` de desenvolvimento declara
`github.com/x448/float16 v0.8.4` além do pgx e do módulo raiz
([go.mod do adaptador][pgvector-go-pgx-mod]). A seleção direta de pgx 5.10.0
prevalece no módulo principal pelo Minimal Version Selection. O adaptador não
foi adicionado, portanto a interação com `float16` ainda não existe no grafo
efetivamente resolvido deste corte.

Há uma pendência de cadeia de suprimentos: o [catálogo do pacote pgx do
pgvector][pgvector-go-pgx-pkg] não detecta licença no submódulo, embora o
repositório upstream tenha MIT. Antes de qualquer alteração em `go.mod`, deve
ser conferida a presença do arquivo de licença no tag 0.4.1 e registrada a
evidência. Se a confirmação falhar, o plano deve parar no módulo raiz e usar
os tipos `Scanner`/`Valuer` com uma decisão explícita, em vez de introduzir um
artefato de licença ambígua.

As consultas ao [Go Vulnerability Database][go-vuln-db] usadas nesta pesquisa
não apontaram advisory aplicável ao `pgvector-go` 0.4.1; isso não substitui uma
varredura do grafo efetivamente resolvido nem garante ausência futura de
vulnerabilidades.

## Biblioteca padrão e alternativas não adotadas

| Necessidade | Escolha deste corte | Por que não adicionar outra dependência |
| --- | --- | --- |
| HTTP da API e do AI Gateway | [`net/http`][stdlib-http] | Já fornece cliente/servidor, timeouts, limites de corpo e shutdown; evita framework e SDK de provedor. |
| JSON de contratos versionados | [`encoding/json`][stdlib-json] | Suficiente para DTOs locais; mantém o contrato do OpenAI isolado no adapter. |
| Migrações próprias embarcadas | [`embed`][stdlib-embed] | Permite empacotar SQL no binário sem biblioteca de migração; o runner continua sob controle do projeto. |
| Acesso SQL genérico, se exigido no futuro | [`database/sql`][stdlib-sql] | Existe como adaptador no pgx, mas não é escolhido sobre a API nativa neste recorte PostgreSQL-only. |
| Hash, arquivos e limites | `crypto/sha256`, `io`, `os` | Biblioteca padrão suficiente para evidência, ingestão e controle de payload. |

Esses pacotes fazem parte da distribuição do Go, não entram no `go.mod` nem
acrescentam dependências externas; `net/http` é distribuído sob BSD-3-Clause.
O [catálogo oficial de `net/http`][stdlib-http] documenta as APIs de cliente e
servidor usadas por este recorte.

ORMs, frameworks web, geradores de SQL, bibliotecas de migração e SDKs de
provedor ficam fora desta tarefa: além de não serem necessários para o corte,
introduziriam abstrações, transitivas e contratos de segurança que não estão
previstos no design aprovado.

## Controles antes da implementação

1. Manter `pgx/v5 v5.10.0` e `pgvector-go v0.4.1` em versões exatas; não usar
   `latest` nem versões abertas.
2. Se o adaptador `pgvector-go/pgx` for considerado futuramente, confirmar a
   licença no tag 0.4.1 e registrar o resultado antes de qualquer `go get`.
3. Fixar o minor do PostgreSQL e o digest da imagem `0.8.6-pg18-bookworm` por
   arquitetura e verificar a imagem base, a extensão e o servidor.
4. Após qualquer atualização de dependências, executar `go mod verify`,
   `go vet ./...`, os testes do módulo e `govulncheck ./...`; executar também a
   verificação de imagem disponível no ambiente.
5. Manter TLS, timeouts de contexto, limites de payload e SQL parametrizado.
   Validar dimensão/perfil do vetor antes de persistir e manter a projeção
   vetorial reconstruível a partir da fonte de verdade.

## Fontes oficiais

[pg-versioning]: https://www.postgresql.org/support/versioning/
[pg-release]: https://www.postgresql.org/about/news/postgresql-186-1711-1615-1519-1424-and-19-beta-3-released-3365/
[pg-license]: https://www.postgresql.org/about/licence/
[pg-platforms]: https://www.postgresql.org/docs/current/supported-platforms.html
[pg-install]: https://www.postgresql.org/docs/current/install-requirements.html
[pgvector-readme]: https://github.com/pgvector/pgvector
[pgvector-changelog]: https://github.com/pgvector/pgvector/blob/master/CHANGELOG.md
[pgvector-license]: https://github.com/pgvector/pgvector/blob/master/LICENSE
[pgvector-security]: https://github.com/pgvector/pgvector/security/policy
[pgvector-tags]: https://hub.docker.com/r/pgvector/pgvector/tags
[pgx-readme]: https://raw.githubusercontent.com/jackc/pgx/v5.10.0/README.md
[pgx-go-mod]: https://raw.githubusercontent.com/jackc/pgx/v5.10.0/go.mod
[pgx-license]: https://raw.githubusercontent.com/jackc/pgx/v5.10.0/LICENSE
[pgx-changelog]: https://raw.githubusercontent.com/jackc/pgx/v5.10.0/CHANGELOG.md
[pgx-pkg]: https://pkg.go.dev/github.com/jackc/pgx/v5@v5.10.0
[go-release]: https://go.dev/doc/devel/release#go1.26.6
[pgvector-go]: https://github.com/pgvector/pgvector-go
[pgvector-go-mod]: https://raw.githubusercontent.com/pgvector/pgvector-go/v0.4.1/go.mod
[pgvector-go-changelog]: https://raw.githubusercontent.com/pgvector/pgvector-go/v0.4.1/CHANGELOG.md
[pgvector-go-license]: https://raw.githubusercontent.com/pgvector/pgvector-go/v0.4.1/LICENSE.txt
[pgvector-go-pgx-mod]: https://github.com/pgvector/pgvector-go/blob/master/pgx/go.mod
[pgvector-go-pgx-pkg]: https://pkg.go.dev/github.com/pgvector/pgvector-go/pgx
[stdlib-http]: https://pkg.go.dev/net/http
[stdlib-json]: https://pkg.go.dev/encoding/json
[stdlib-embed]: https://pkg.go.dev/embed
[stdlib-sql]: https://pkg.go.dev/database/sql
[GO-2026-4771]: https://pkg.go.dev/vuln/GO-2026-4771
[GO-2024-2567]: https://pkg.go.dev/vuln/GO-2024-2567
[GO-2024-2606]: https://pkg.go.dev/vuln/GO-2024-2606
[go-vuln-db]: https://pkg.go.dev/vuln/
