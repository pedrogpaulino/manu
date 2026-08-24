# Corpus de referência do primeiro corte vertical

Este documento fixa o manifesto documental do primeiro corte do `Knowledge
Engine`. Ele registra identidades, revisões ou hashes, finalidade, autorização,
inclusões, exclusões, profundidade e lacunas das três fontes locais observadas.
Não copia os repositórios, não extrai os pacotes CAR para este projeto e não
registra valores de segredos.

**`Analysis Snapshot` do manifesto:** 2026-08-17, inspeção local somente leitura.

## Navegação

- [Formato e regras do manifesto](#formato-e-regras-do-manifesto)
- [Papéis e autorização](#papéis-e-autorização)
- [Ticketmaster — referência Java/Quarkus](#ticketmaster--referência-javaquarkus)
- [CarbonApps — amostra declarativa WSO2](#carbonapps--amostra-declarativa-wso2)
- [ERPNext — inventário e escala](#erpnext--inventário-e-escala)
- [Goldens executáveis locais](#goldens-executáveis-locais)
- [Critérios de seleção e uso](#critérios-de-seleção-e-uso)

## Formato e regras do manifesto

Cada entrada versionada do corpus deve manter, no mínimo, os campos abaixo.
Os nomes são campos conceituais do manifesto; não prescrevem um formato físico
ou uma implementação de ingestão.

| Campo | Conteúdo exigido |
| --- | --- |
| `source_id` | Identificador estável do recorte (`ticketmaster-java-quarkus`, `wso2-car-sample` ou `erpnext-inventory`). |
| `location` | Caminho local autorizado ou referência de origem; o caminho não substitui a identidade. |
| `identity` | Repositório, diretório ou pacote e seu papel no corpus. |
| `source_revision` / `artifact_hash` | Revisão Git, hash de árvore, hash do manifesto de diretório ou SHA-256 do `Artifact`. |
| `analysis_snapshot` | Data, escopo e método da observação que produziu o registro. |
| `authorization` | O que foi autorizado para leitura, processamento e eventual transferência; autorizações e condicionantes devem ser registradas explicitamente. |
| `include` / `exclude` | Artefatos e dimensões incluídos e excluídos, com motivo. |
| `evaluation_role` | Referência primária, heterogeneidade declarativa ou inventário/escala. |
| `depth` | Profundidade tentada por dimensão, sem prometer uniformidade. |
| `expected_gaps` | `Explicit Gap`s esperadas antes de uma análise. |
| `transfer_policy` | Se há ou não autorização separada para enviar conteúdo ao `AI Gateway` ou a provedor externo. |

Revisões e hashes registrados abaixo são evidências daquilo que foi observado,
não uma autorização para alterar as fontes. Uma execução futura deve registrar
novamente a revisão ou o hash efetivamente usado; não deve assumir que o
ponteiro local continua igual.

### Integridade e não cópia

- O corpus permanece nos três caminhos externos indicados nas entradas; este
  arquivo contém somente metadados, nomes indispensáveis de artefatos
  selecionados e hashes.
- O processamento confirmado para este manifesto é inspeção estrutural,
  listagem, leitura estática limitada e cálculo de hashes em modo somente
  leitura.
- Nesta inspeção, nenhum conteúdo de fonte foi enviado a um modelo: chaves,
  credenciais, tokens, arquivos de ambiente, relatórios gerados e
  configurações sensíveis não foram copiados nem transferidos.
- Em uma execução autorizada, somente trechos ou evidências selecionados,
  sanitizados e aprovados pelas políticas podem ser enviados à OpenAI API por
  meio do `AI Gateway`, para embeddings ou geração. O modelo nunca recebe
  acesso direto ao diretório.
- A existência de uma revisão ou hash não afirma que houve `Observed
  Execution`, implantação conhecida ou autorização para transferir conteúdo
  fora desse protocolo.

## Papéis e autorização

| Faixa | Fonte | Papel de avaliação | Profundidade do primeiro corte |
| --- | --- | --- | --- |
| Correção semântica | Ticketmaster | Referência primária para inventário Java/Quarkus, relações e `Possible Flow`s estáticos. | Maior profundidade: símbolos, endpoints, serviços, entidades, estados, exceções e configuração referenciada. |
| Heterogeneidade | Amostra de seis CARs em `carbonapps` | Verificar abertura segura, inventário de artefatos e referências declarativas WSO2. | Inventário e relações declarativas mínimas; sem semântica profunda de runtime WSO2. |
| Escala | ERPNext completo, com recorte pedido a faturamento | Exercitar inventário amplo e fallback genérico; fornecer contraste de linguagem/framework. | Inventário completo e relações estáticas do recorte; sem semântica Python/Frappe profunda. |

### Autorização confirmada

A autorização confirmada para esta versão é a leitura e inspeção local, em modo
somente leitura, dos três caminhos solicitados. Hashing, contagem, listagem de
entradas e leitura estática necessária ao manifesto estão dentro desse escopo.

A transferência externa também está autorizada de forma condicionada: uma
execução explícita pode enviar à OpenAI API, por meio do `AI Gateway`, somente
trechos ou evidências selecionados, sanitizados e aprovados pelas políticas de
instalação, fonte e usuário, para embeddings e geração. Essa autorização não
inclui:

- copiar qualquer corpus ou conteúdo excluído para este repositório;
- enviar segredos, credenciais, tokens, valores sensíveis ou conteúdo fora do
  recorte;
- conceder ao modelo acesso direto ao diretório ou às fontes originais;
- executar a aplicação, um site Frappe, um runtime WSO2 ou infraestrutura
  associada sem uma execução explicitamente autorizada.

Esta inspeção não realizou chamada real à OpenAI API. O [protocolo de avaliação
do primeiro corte](first-vertical-slice-evaluation.md) detalha o registro da
autorização, sanitização, orçamento e execução `live eval`.

## Ticketmaster — referência Java/Quarkus

### Identidade e revisão

- **Origem:** `/home/pedro_paulino/projetos/doc/system-design-interview-ticketmasters`.
- **Identidade:** repositório Git local, branch `main`, estado limpo no momento
  da inspeção.
- **`Source Revision`:** `88cab04c59c58e745a94302e5c9e856830c4c902` (commit de
  2026-07-06; merge de `develop`).
- **Hashes de árvore observados:** `app` =
  `60b542550ec0497ada2d280baa151a46aa4b701c`; `app/src` =
  `be5d7d30af1f5809f52b9d070be350918eae7ed6`; `app/src/main/java` =
  `ec45de18c876eb4c2df7df8e681145efb786ece7`; `app/src/main/resources` =
  `361ec4acd0daf02dc4c0a5f85f0e129775bfbfc6`; `app/src/test` =
  `2946823369082e83f4171cac76054c978540cd91`.
- **Sinais de contexto:** `app/pom.xml` declara Java 21 e Quarkus 3.26.4;
  foram observados 61 arquivos Java principais, 3 recursos e 5 arquivos de
  teste.

### Inclusões e exclusões

Incluído para a referência estática:

- fontes Java em `app/src/main/java`, abrangendo `controller`, `entity`,
  `service`, `exception` e `listener`;
- testes em `app/src/test` apenas como material de comportamento esperado;
- estrutura e nomes de propriedades dos recursos, com valores sensíveis
  removidos da fronteira de análise;
- `app/pom.xml` para identificar Java, Quarkus e extensões declaradas.

Excluído:

- relatórios de performance e outros artefatos gerados;
- material de chaves, credenciais, tokens e arquivos de configuração cujo
  valor possa conceder acesso;
- estado de banco, filas, LocalStack, ECS, AWS, implantação e qualquer
  ambiente executável.

### Áreas tentadas, profundidade e lacunas

| Dimensão | Evidência estática e profundidade | `Explicit Gap` esperada |
| --- | --- | --- |
| Inventário e estrutura | 61 fontes Java organizadas em controllers, entidades, serviços, exceções e listener; DTOs e recursos identificáveis. | Não há inventário do ambiente implantado nem dependências efetivamente carregadas em runtime. |
| Entidades e relações | Relações controller → service → entity/DTO e persistência Panache; estados de evento, assento, reserva e ticket. | Não há confirmação de schema, cardinalidade efetiva ou dados vivos. |
| `Possible Flow` e decisões | Criação/listagem de eventos e assentos; reserva com bloqueio pessimista, criação de tickets e atualização de assentos; confirmação/rejeição e expiração assíncrona por SQS; autenticação e papéis. | Sem telemetria ou registro operacional, não se afirma `Observed Execution`; intenção de negócio permanece lacuna. |
| Configuração e contexto | Chaves de configuração para PostgreSQL, JWT, SQS, perfis Quarkus, expiração e observabilidade, sem valores. | `Environment`, `Deployment`, `Build Artifact` efetivo e `Configuration State` real são desconhecidos. |
| Erros e evolução | Exceções de recurso ausente, assento ocupado, login e atualização de reserva são inventariadas; a revisão Git fixa uma linha de base. | Não há comparação entre releases nem análise de causa em produção. |

Esta faixa tem a maior profundidade semântica do corte, mas continua sendo uma
análise estática e parcial de Java/Quarkus. Ela não é uma promessa de cobertura
uniforme de AWS, PostgreSQL, SmallRye, segurança ou comportamento operacional.

## CarbonApps — amostra declarativa WSO2

### Identidade e revisão do diretório

- **Origem:** `/home/pedro_paulino/projetos/doc/carbonapps`.
- **Identidade:** diretório local sem metadados Git; a revisão é um snapshot de
  arquivos CAR observado em 2026-08-17.
- **Inventário observado:** 132 arquivos `*.car`.
- **Hash do manifesto do diretório:**
  `23eca6b8f6efdb9e8e671678953c983d6f911d614ca539f5d397c545452a3943`.
  Para reproduzir, ordene lexicograficamente os 132 nomes, preserve espaços,
  calcule o SHA-256 de cada CAR e emita uma linha UTF-8 no formato
  `<nome><espaço><sha256>\n`; calcule então o SHA-256 do fluxo dessas linhas.
  Esse digest não substitui os hashes individuais abaixo.
- **Método:** identificação como arquivo ZIP e leitura do diretório central
  (`unzip -Z1`) para inventário; nenhum CAR foi extraído ou copiado.

### Seleção de seis CARs

| `Artifact` selecionado | SHA-256 | Entradas declarativas observadas | Justificativa de diversidade |
| --- | --- | --- | --- |
| `ERPProxyServiceCompositeApplication_1.0.0.car` | `f23368236afe6890b76544be3978b17862c99a140aff467f881887569460720f` | Proxy, endpoint XML, WSDL, `artifact.xml` e `registry-info.xml` (14 entradas). | Abre a amostra com proxy, endpoint, contrato WSDL e registro. |
| `FIESCArchitectureConfigApplication_1.0.0.car` | `7c14d81238d42cf9635b3d01721de5f7114ba2c4ef4312f2f6394ccd413db61b` | Sequências de login/logout e fault handler, com XML de artefato (10 entradas). | Representa configuração e fluxo declarativo de sequência. |
| `FIESCArchitectureRegistryApplication_1.0.0.car` | `ca58026fdf610cf97d8340f2737f01828433eaa418247b933d55cc78f3f13048` | Transformações XSLT em recursos e informações de registro (16 entradas). | Cobre transformação e recurso de registry sem repetir o tipo de proxy. |
| `DocumentosIntegradosDSSApplication_1.0.0.car` | `d8eb71542b09ef81e06238aaffec7b6eed13bc09e31c35023f9604607069a937` | Data service `.dbs` e `artifact.xml` (4 entradas). | Representa serviço de dados declarativo e o menor pacote estrutural. |
| `EcommerceSESICompositeApplication_1.0.0.car` | `0ce305204456bf258c7f3a6417ddf80eb3a8c91991cffc672f292c43de2fc63f` | Vários proxies, endpoints e WSDLs (40 entradas). | Exercita composição, repetição de tipos e referências entre artefatos. |
| `NotaFiscalCompositeApplication_2.0.0.car` | `9d54deef4bf306cbee9ca0f49b848b5f28b7c83d28acd684089ae8444fe58999` | API, metadata YAML, sequências in/out/fault, templates, XSLT e DSS (86 entradas). | Maior variedade declarativa: API, metadata, transformação, sequência e dados. |

A amostra cobre abertura de pacote, inventário de `Artifact`s e referências
declarativas mínimas por nomes e tipos observáveis. Não se deve inferir a
semântica de uma referência apenas por seu nome; a evidência de conteúdo fica
fora deste manifesto e só pode ser enviada à OpenAI API depois de seleção,
sanitização e aplicação das políticas autorizadas.

### Profundidade e lacunas

- **Tentado:** inventário genérico de pacotes e entradas; identificação de
  proxies, endpoints, APIs, WSDL, XSLT, sequências, metadata, registry e DSS;
  correlação nominal entre artefatos que compartilham o pacote.
- **Não prometido:** interpretação profunda de mediators, expressões,
  configurações de runtime, contratos completos, políticas ou semântica WSO2.
- **Lacunas:** nenhuma `Observed Execution`; não há `Environment`,
  `Deployment`, estado de registry ou configuração efetiva; 126 dos 132 CARs
  permanecem fora da amostra sem serem tratados como ausência no ambiente.
- **Exclusões:** os 126 CARs não selecionados, conteúdo interno não necessário
  ao inventário e segredos. Nesta inspeção, nenhum pacote foi extraído ou
  transferido; uma avaliação autorizada só pode transferir evidências
  selecionadas e sanitizadas pelo `AI Gateway`.

## ERPNext — inventário e escala

### Identidade e inventário completo

- **Origem:** `/home/pedro_paulino/projetos/doc/erpnext`.
- **Identidade:** repositório Git local, branch `develop`, estado limpo na
  inspeção.
- **`Source Revision`:** `1f839061899c019b9a326b960fc5d10b4b34c761`, commit de
  2026-08-17; `erpnext/__init__.py` declara `17.0.0-dev`.
- **Hash de árvore:** `erpnext` =
  `1061f78cfea996cbaa604e9d71c3ae5298f14fcb`.
- **Manifesto de módulos:** `erpnext/modules.txt`, SHA-1 Git
  `b8b12e90fb081429822839977f12fa318a43d244`, 21 nomes (o último não termina
  com quebra de linha).
- **Tamanho observado:** 5.316 arquivos rastreados sob `erpnext`, dos quais
  2.940 são Python. O inventário completo da revisão é o conjunto rastreado
  pela árvore acima, não uma cópia no Manu.

Os 21 nomes de módulo declarados em `modules.txt` são:

`Accounts`, `CRM`, `Buying`, `Projects`, `Selling`, `Setup`, `Manufacturing`,
`Stock`, `Support`, `Utilities`, `Assets`, `Portal`, `Maintenance`, `Regional`,
`ERPNext Integrations`, `Quality Management`, `Communication`, `Telephony`,
`Bulk Transaction`, `Subcontracting` e `EDI`.

### Recorte funcional: pedido a faturamento

O recorte usa o inventário completo como faixa de escala, mas limita a
profundidade funcional ao caminho estático de pedido a faturamento:

| Etapa | Artefatos e evidência de referência | Tratamento no primeiro corte |
| --- | --- | --- |
| Preparação do pedido | `Customer`, `Item`, preço/estoque e `Sales Order`/`Sales Order Item` em `erpnext/selling/doctype/` e `erpnext/stock/doctype/item/`. | Inventário e relações nominais; regras Python/Frappe profundas ficam fora. |
| Separação/entrega | `make_delivery_note` em `erpnext/selling/doctype/sales_order/mapper.py`, `Delivery Note` e itens em `erpnext/stock/doctype/delivery_note/`. | Relação declarativa estática entre pedido e entrega; sem execução. |
| Faturamento | `make_sales_invoice` em `erpnext/selling/doctype/sales_order/mapper.py` e `erpnext/stock/doctype/delivery_note/mapper.py`, além de `Sales Invoice` e itens em `erpnext/accounts/doctype/sales_invoice/`. | Relação estática entre pedido/entrega e invoice; evidências e lacunas explícitas. |
| Pagamento relacionado | `Payment Entry` e `Payment Entry Reference` em `erpnext/accounts/doctype/payment_entry/`. | Apenas contexto adjacente ao faturamento; não é profundidade obrigatória do corte. |

O caminho acima é uma reconstrução de código e declarações; é um `Possible
Flow`, não um `Observed Execution`. Sem site, banco, `Environment`,
`Deployment` ou telemetria vinculados, o manifesto não afirma que um pedido foi
entregue, faturado ou pago.

### Profundidade e lacunas

- **Tentado:** inventário completo da aplicação, identificação de módulos e
  doctype, referências nominais e mapeadores do caminho pedido → entrega →
  faturamento.
- **Fora da profundidade inicial:** semântica Python/Frappe profunda, execução
  do ORM, hooks, permissões efetivas, cálculo de impostos, estoque real,
  estados de site e comportamento de produção.
- **Lacunas:** não há site Frappe, banco, dados de documentos, `Configuration
  State`, `Environment`, `Deployment`, telemetria ou `Observed Execution` no
  manifesto; arquivos de ambiente e valores sensíveis não são processados.
- **Exclusões:** execução do ERPNext, banco/site, caches e artefatos gerados,
  credenciais e configurações locais sensíveis; a árvore externa continua no
  seu repositório de origem.

## Goldens executáveis locais

Os analisadores mantêm fixtures locais pequenas para reproduzir a normalização
factual das três famílias. Os testes vinculados decodificam os goldens com
schema estrito, comparam a saída gerada e verificam ordenação, locadores e
determinismo do digest sob repetição e inversão da entrada. O cenário WSO2
monta um CAR temporário a partir dos dois XMLs; o CAR não é vendorizado.

| Família | Fixtures, teste e golden | Digest factual congelado | Fatos | Cobertura agregada (`dimensão=estado: contagem`) |
| --- | --- | --- | ---: | --- |
| Java/Quarkus | [`BookingResource.java`](../../internal/analyzer/java/testdata/quarkus3/BookingResource.java); [`normalization_integration_test.go`](../../internal/analyzer/java/normalization_integration_test.go); [`facts.golden.json`](../../internal/analyzer/java/testdata/quarkus3/facts.golden.json) | `528a6670e30f2074548c63516046b58a61b5bca2e38ce9974e176f09c3554efb` | 32 | `configuration_variations=produced: 1`; `entities_and_relationships=produced: 7`; `flows_and_dependencies=produced: 10`; `landscape_inventory_structure=produced: 2` |
| WSO2 | [`api-v1.xml`](../../internal/analyzer/wso2/testdata/api-v1.xml), [`shared-v1.xml`](../../internal/analyzer/wso2/testdata/shared-v1.xml); [`normalization_integration_test.go`](../../internal/analyzer/wso2/normalization_integration_test.go); [`facts.golden.json`](../../internal/analyzer/wso2/testdata/facts.golden.json) | `1861914ca608c8fca2c4add57f7d7b43e6a711703431ecb41c3d7e6fb80a86db` | 23 | `configuration_variations=produced: 2`; `entities_and_relationships=produced: 8`; `flows_and_dependencies=incomplete: 2`; `flows_and_dependencies=produced: 7` |
| Python/Frappe | [`doctype.py`](../../internal/analyzer/python/testdata/frappe17/doctype.py), [`hooks.py`](../../internal/analyzer/python/testdata/frappe17/hooks.py); [`normalization_integration_test.go`](../../internal/analyzer/python/normalization_integration_test.go); [`facts.golden.json`](../../internal/analyzer/python/testdata/frappe17/facts.golden.json) | `9eab722a13516e68b60d097b5c425e0dc1247df6ad330a23175249cc927ef9ab` | 31 | `configuration_variations=produced: 11`; `entities_and_relationships=produced: 3`; `flows_and_dependencies=produced: 8`; `landscape_inventory_structure=produced: 2` |

As contagens de cobertura são os agregados dimensão/estado registrados no
golden e não devem ser somadas como se fossem a contagem de fatos. Esses
fixtures locais são apenas material executável de verificação: não substituem
o corpus externo não vendorizado descrito neste documento, não copiam o
conteúdo das fontes e não carregam payloads, segredos ou valores dinâmicos nos
goldens. Também não alegam completude semântica, equivalência entre famílias
ou cobertura operacional das fontes externas.

## Critérios de seleção e uso

A seleção foi considerada adequada ao primeiro corte porque mantém as três
faixas com papéis não intercambiáveis:

1. Ticketmaster fornece uma referência pequena e verificável para semântica
   Java/Quarkus, com fluxos estáticos, estados e exceções que podem formar
   perguntas de competência determinísticas.
2. Os seis CARs variam entre proxy/endpoint/WSDL, sequência, XSLT/registry,
   DSS, composição e API/metadata, permitindo testar inventário e referências
   declarativas sem exigir um parser WSO2 completo.
3. ERPNext mantém escala e contraste de linguagem/framework, mas o recorte
   funcional evita transformar semântica Python/Frappe em critério de sucesso
   prematuro.

Em qualquer avaliação, a cobertura deve ser comparada somente entre dimensões
tentadas no recorte correspondente. A presença no manifesto não implica
compreensão completa, suporte uniforme ou atualização contínua. A transferência
à OpenAI API permanece limitada ao protocolo autorizado de seleção,
sanitização e política; revisões, hashes, exclusões e lacunas acima devem
acompanhar a execução para que uma resposta gerada permaneça distinguível de
evidência observada e de conhecimento curado.
