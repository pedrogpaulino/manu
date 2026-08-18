# Protocolo de avaliação do primeiro corte vertical

Este documento define como registrar, repetir e comparar a avaliação do
primeiro corte do `Knowledge Engine`. Ele é um protocolo lógico de avaliação;
não é um contrato de API, um formato físico de persistência, uma implementação
de analisador ou uma regra codificada para responder perguntas.

O protocolo segue o recorte de produto em [PRODUCT.md](../../PRODUCT.md), as
fronteiras de [ARCHITECTURE.md](../../ARCHITECTURE.md) e a [especificação
principal de compreensão](../../openspec/specs/knowledge-engine-comprehension/spec.md),
usando os termos do [domínio canônico](../../DOMAIN.md). O vínculo de identidade
e autorização das fontes está no [manifesto do corpus](first-vertical-slice-corpus.md).

## 1. Escopo e estados do conhecimento

O primeiro corte tem três papéis de referência diferentes:

| Recorte | Papel na avaliação | Profundidade esperada no corte |
| --- | --- | --- |
| Ticketmaster | Referência primária de correção semântica Java/Quarkus, fluxos, decisões, configurações e erros sustentados por código e documentos autorizados. | Inventário, símbolos Java, endpoints, chamadas diretas, configurações referenciadas, exceções e `Possible Flow`s mínimos. |
| Amostra WSO2 | Referência de heterogeneidade declarativa e de middleware, com quatro a seis CARs de tipos distintos. | Abertura segura, inventário de membros e referências diretas entre APIs, proxies, sequences, recursos, WSDL e XSD quando observáveis. |
| ERPNext | Referência de inventário, volume e escala, incluindo o recorte funcional de pedido a faturamento. | Fallback genérico para inventário e recuperação textual; semântica Python/Frappe profunda fica explicitamente não suportada. |

O manifesto do corpus é a autoridade para caminho local, autorização, revisão,
hash, inclusões e exclusões. Este protocolo não repete conteúdo das fontes e
não inventa uma revisão ou um hash quando o manifesto ainda não os vinculou.
Uma execução sem esse vínculo não pode ser apresentada como avaliação
reproduzível.

Os estados epistemológicos permanecem separados:

- `Observed knowledge` é a observação sustentada por um `Artifact`, seu
  `Source Revision`, método, instante, `Evidence` e `Provenance`;
- `Generated knowledge` é a resposta ou síntese produzida por um modelo ou
  transformação, sempre limitada ao pacote de evidências autorizado;
- `Curated knowledge` é a pergunta, a resposta de referência e as afirmações
  aceitáveis revisadas por pessoas identificadas.

Uma resposta gerada não se torna evidência por ser fluente. Uma referência
curada também não apaga observações conflitantes; ela registra o tratamento
esperado e os critérios usados para revisá-lo.

## 2. Registro versionado de caso de avaliação

Cada caso é uma unidade estável de comparação. A tabela abaixo descreve os
campos obrigatórios do registro lógico; a serialização será escolhida na
implementação sem alterar seu significado.

| Campo | Conteúdo obrigatório | Regra de avaliação |
| --- | --- | --- |
| `case_id` | Identificador estável do caso. | Não muda quando uma nova versão é publicada. |
| `case_version` | Versão do caso e estado (`draft`, `curated` ou `retired`). | Incrementa quando muda a pergunta, o recorte, a referência ou o critério. |
| `corpus_id` e `corpus_revision` | Identidade lógica do corpus e revisão do manifesto. | Deve apontar para uma revisão imutável do manifesto. |
| `source_revision` | Revisão ou hash efetivamente analisado para cada fonte. | Ausência ou desconhecimento é uma `Explicit Gap`, não um valor inferido. |
| `scope` | Público, finalidade, fontes, artefatos e dimensões tentadas. | Torna comparáveis somente execuções com escopo equivalente. |
| `inclusions` e `exclusions` | Itens incluídos, itens excluídos e razão de cada exclusão. | Material sensível, relatórios gerados ou conteúdo não autorizado não entram silenciosamente no caso. |
| `authorization_ref` | Referência não secreta à autorização de processamento e transferência. | Nunca contém credencial, token, cabeçalho ou segredo. |
| `audience` | Público que usaria a resposta: sustentação, arquitetura, negócio ou curadoria. | Orienta a interpretação da pergunta, não altera sua evidência. |
| `competence_question` | Pergunta versionada em linguagem de avaliação. | É instrumento de teste; não pode ser transformada em instrução fixa do engine. |
| `reference_answer` | Resposta curada, seu contexto e sua redação aceitável. | Deve distinguir fatos observados, inferências permitidas e lacunas. Igualdade textual não é requisito. |
| `authors` e `reviewers` | Autoria, revisão, data e estado de aprovação da referência. | Uma referência sem autoria ou revisão não é baseline curada. |
| `acceptable_claims` | Afirmações que a evidência permite sustentar. | Cada afirmação recebe origem esperada (`Observed`, `Generated` ou `Curated`) e contexto. |
| `expected_evidence` | Locadores e predicados que precisam ser encontrados. | Descreve onde verificar, sem copiar o conteúdo da fonte. |
| `expected_gaps` | Lacunas que a resposta deve declarar. | Inclui ausência de telemetria, especialização, vínculo temporal ou autorização. |
| `applicable_analyzers` | Analisadores aplicáveis, não aplicáveis e ainda não suportados. | Permite comparar somente dimensões tentadas em cada recorte. |
| `criteria` | Correção, cobertura, rastreabilidade, atualidade, incerteza, abstinência e critérios de operação aplicáveis. | Critérios são avaliados independentemente; não formam uma confiança única. |
| `created_at`, `updated_at` e `supersedes` | Histórico de criação, atualização e substituição. | Uma versão antiga permanece recuperável para explicar regressões. |

Um caso só passa a `curated` depois de revisão humana da pergunta, da resposta
de referência e dos locadores de evidência. Alterar a referência cria uma nova
versão; não se reescreve o baseline usado em uma execução anterior. O resultado
da execução deve acrescentar `run_id`, `analysis_snapshot`, estado de cobertura,
falhas, resposta obtida, evidências recuperadas e métricas, sem alterar o caso.

### Locadores de evidência

Os locadores são verificáveis sem transportar a fonte para este repositório.
Eles combinam, conforme o tipo de artefato:

- `Artifact` identificado por fonte, revisão/hash, caminho relativo ou membro
  de pacote e tipo;
- símbolo, anotação, endpoint, chave de configuração, exceção ou intervalo de
  linhas, quando a fonte permitir esse nível de localização;
- caminho XML e atributo de referência para CARs, WSDL ou XSD;
- registro do manifesto, `Observation`, `Evidence` ou `Provenance` relacionado;
- decisão de autorização ou `Explicit Gap` quando o conteúdo não puder ser
  visualizado.

Um locador deve ser suficiente para uma pessoa autorizada reabrir a evidência
na fonte original. Um hash confirma identidade; não substitui o conteúdo ou a
proveniência. Quando o usuário avaliador não puder ver a evidência, o resultado
registra `Evidence protected`, preservando a distinção de uma afirmação sem
suporte.

## 3. Perguntas iniciais de competência

As perguntas seguintes formam o banco inicial. Elas definem o que verificar na
referência, não como o engine deve produzir uma resposta. Novas perguntas podem
ser acrescentadas por versão do banco sem transformar uma resposta esperada em
uma regra de comportamento.

| ID | Pergunta | Recortes | Evidência e critério principal |
| --- | --- | --- | --- |
| `Q-INV-01` | Quais sistemas, aplicações, serviços, componentes, fontes e documentos existem no recorte e como se organizam? | Todos | Manifesto e inventário de `Artifact`s; cobertura e exclusões explícitas. |
| `Q-REL-01` | Quais entidades têm relações diretas ou dependências no recorte e qual fonte sustenta cada relação? | Ticketmaster, WSO2 e ERPNext quando observável | Símbolo/caminho de código, membro XML ou documento relacionado; nenhuma relação apenas inferida por nome. |
| `Q-FLOW-01` | Qual `Possible Flow` pode ocorrer, em que condições e com quais dados? Há `Observed Execution` ou apenas interpretação de `Business Process`? | Ticketmaster e WSO2 | Chamadas, referências e condições observadas; ausência de telemetria permanece lacuna. |
| `Q-DEC-01` | Quais decisões, condições e origens de dados alteram o caminho possível? | Ticketmaster e WSO2 | Ramo de código, configuração, contrato ou referência declarativa; justificativa de negócio ausente é declarada. |
| `Q-CON-01` | O que varia por configuração, ambiente, release, feature flag ou documentação e quais contextos são desconhecidos? | Todos | `Source Revision`, `Analysis Snapshot`, `Environment`, `Configuration State` e demais contextos disponíveis. |
| `Q-CAP-01` | Que `Capability` o ambiente oferece e como ela pode ser acessada? | Ticketmaster, WSO2 e ERPNext quando o inventário sustentar | Endpoint, API, proxy, relatório ou recurso e suas entradas/saídas; não confundir com `Knowledge Product`. |
| `Q-ERR-01` | Onde erros podem surgir, propagar-se ou alterar um `Possible Flow`? | Ticketmaster e WSO2 | Exceções, respostas, configuração e relações de propagação observáveis; não afirmar causa raiz automática. |
| `Q-EVD-01` | O que sustenta cada afirmação, qual é sua `Provenance` e qual estado de cobertura permanece? | Todos | Locadores consultáveis, origem observado/gerado/curado, contestação e `Analysis Coverage`. |
| `Q-ABS-01` | O que o Manu não pode concluir com o suporte disponível e por que deve se abster? | Todos | `Explicit Gap`, autorização, ausência de especialização, contexto desconhecido ou evidência insuficiente. |
| `Q-SCALE-01` | Qual é o inventário do recorte e como tempo, armazenamento e trabalho variam quando seu volume aumenta? | ERPNext | Contagens e métricas do manifesto/análise; não exige semântica Python/Frappe profunda. |

Uma avaliação pode selecionar apenas as perguntas aplicáveis ao recorte. O
resultado registra também as perguntas não aplicáveis e não suportadas para que
uma seleção menor não pareça uma compreensão completa.

## 4. Referências inicialmente verificáveis

As referências abaixo são envelopes curados de verificação. O manifesto deve
ligá-los à revisão/hash e aos caminhos concretos antes da execução. Onde este
documento não conhece o nome de um símbolo ou membro, ele exige que o locador
seja fornecido pelo manifesto/análise; não cria um nome fictício.

### 4.1 Ticketmaster — Java/Quarkus

| Caso | Resposta de referência aceitável | Evidências esperadas | Lacunas obrigatórias |
| --- | --- | --- | --- |
| `TM-INV-01` | Identifica a fronteira da aplicação, artefatos Java/Quarkus, endpoints, componentes, configurações e exceções que existem no recorte autorizado. | Registro do manifesto; lista de `Artifact`s; locadores de símbolos/anotações/endpoints e configuração, cada um ligado à revisão/hash. | Itens excluídos, relatórios gerados, material sensível e qualquer área não analisada. |
| `TM-FLOW-01` | Descreve somente o `Possible Flow` sustentado por uma entrada, chamadas diretas, condições e dados observados; não o apresenta como execução em runtime. | Locadores de método/endpoint e chamadas; condições de código; `Observation`, `Evidence` e `Provenance` de cada trecho. | Falta de logs, métricas ou traces para `Observed Execution`; intenção de negócio não encontrada. |
| `TM-DEC-01` | Separa como uma decisão é executada, quais condições/configurações a influenciam e por que a escolha de negócio/arquitetura não está documentada, quando for o caso. | Ramos, contratos, chaves de configuração e documentos existentes; autoria separada para qualquer justificativa curada. | Justificativa ausente, configuração não vinculada ou fonte protegida. |
| `TM-ERR-01` | Enumera erros e propagação que a fonte permite reconstruir, sem declarar diagnóstico automático de causa raiz ou execução ocorrida. | Declarações, tratamento de exceções, respostas e relações de chamada autorizadas. | Comportamento operacional não observado e caminhos que dependem de contexto ausente. |

O critério de correção do Ticketmaster é por afirmação e evidência: uma
resposta pode ser parcialmente correta, desde que omissões, incertezas e
`Explicit Gap`s sejam preservadas. Um `Possible Flow` não é `Observed
Execution` apenas porque o código contém a chamada correspondente.

Na `Source Revision` `88cab04c59c58e745a94302e5c9e856830c4c902`, os locadores
concretos mínimos observados para `TM-FLOW-01` são:

| Papel no caminho estático | Locador observado | O que sustenta |
| --- | --- | --- |
| Endpoint/controller | `app/src/main/java/tech/buildrun/controller/BookingController.java:20-41`, `BookingController#createBooking` | `POST` em `/bookings`, autorização declarada e chamada direta a `BookingService#createBooking`. |
| Serviço | `app/src/main/java/tech/buildrun/service/BookingService.java:32-63`, `BookingService#createBooking` | Validação, seleção de assentos, criação da reserva, tickets, atualização de assentos e agendamento de expiração como caminho possível. |
| Reserva/entidade | `app/src/main/java/tech/buildrun/entity/BookingEntity.java:11-26`, `BookingEntity` | Entidade de reserva, usuário, estado e instante de reserva observáveis no código. |
| Assento/entidade | `app/src/main/java/tech/buildrun/entity/SeatEntity.java:6-21`, `SeatEntity` | Relação declarada com evento e estado do assento usado pelo serviço. |
| Erro | `app/src/main/java/tech/buildrun/exception/SeatAlreadyBookedException.java:3-24`, `SeatAlreadyBookedException` | Ramo de assento já reservado e resposta de erro declarada. |

Esses locadores sustentam uma reconstrução estática e `Possible Flow`; não são
evidência de `Observed Execution`, de dados vivos ou de intenção de negócio.
Qualquer mudança da revisão/hash deve revalidar os locadores antes de reutilizar
essa referência.

### 4.2 WSO2 — amostra de CARs

A seleção concreta de quatro a seis CARs, seus hashes e seus membros pertence
ao manifesto do corpus. A amostra deve cobrir, na medida em que os CARs
disponíveis permitirem, tipos como API, proxy, sequence, recurso/registry,
WSDL e XSD. Os IDs abaixo identificam casos de avaliação, não nomes ou hashes
de casos; os nomes e hashes dos seis artefatos fixados para esta revisão são:

| `Artifact` do manifesto | SHA-256 observado |
| --- | --- |
| `ERPProxyServiceCompositeApplication_1.0.0.car` | `f23368236afe6890b76544be3978b17862c99a140aff467f881887569460720f` |
| `FIESCArchitectureConfigApplication_1.0.0.car` | `7c14d81238d42cf9635b3d01721de5f7114ba2c4ef4312f2f6394ccd413db61b` |
| `FIESCArchitectureRegistryApplication_1.0.0.car` | `ca58026fdf610cf97d8340f2737f01828433eaa418247b933d55cc78f3f13048` |
| `DocumentosIntegradosDSSApplication_1.0.0.car` | `d8eb71542b09ef81e06238aaffec7b6eed13bc09e31c35023f9604607069a937` |
| `EcommerceSESICompositeApplication_1.0.0.car` | `0ce305204456bf258c7f3a6417ddf80eb3a8c91991cffc672f292c43de2fc63f` |
| `NotaFiscalCompositeApplication_2.0.0.car` | `9d54deef4bf306cbee9ca0f49b848b5f28b7c83d28acd684089ae8444fe58999` |

O manifesto observado nesta versão sustentou a identidade, a leitura do
diretório central e o inventário dos membros; não houve ainda análise do
conteúdo XML interno. A autorização confirmada permite que uma análise futura
leia localmente o snapshot dentro do escopo. Uma transferência ao `AI Gateway`
ou a um provedor exige autorização independente e pode enviar somente trechos
sanitizados que essa política permita. Portanto, `WSO2-REL-01` e `WSO2-CON-01`
permanecem casos de evidência futura porque ainda não há locadores produzidos
por um analisador autorizado, não por falta geral de autorização: relações
entre APIs, proxies, sequences, WSDL e XSD só entram na resposta quando o
locador e o conteúdo correspondente forem observados. Não se deve inferir uma
relação a partir do nome do CAR.

| Caso | Resposta de referência aceitável | Evidências esperadas | Lacunas obrigatórias |
| --- | --- | --- | --- |
| `WSO2-INV-01` | Para cada CAR selecionado, identifica o hash da revisão analisada, abre o pacote sem executar conteúdo e lista os membros e tipos observáveis. | Registro do manifesto, hash, caminho do membro e tipo declarado; resultado de abertura segura. | Membros excluídos, formato não reconhecido e qualquer conteúdo não autorizado. |
| `WSO2-REL-01` | Relaciona APIs, proxies, sequences e recursos somente quando a referência declarativa literal estiver presente; não infere uma relação por semelhança de nome. | Caminho XML, atributo de referência e hash do artefato que declara e do artefato referido. | Referência indireta, dinâmica ou ausente; execução operacional não observada. |
| `WSO2-CON-01` | Identifica importações/includes e vínculos diretos entre WSDL, XSD e artefatos que os referenciam quando o XML os sustentar. | XPath/atributo de importação ou include, membro do CAR e `Provenance`. | Esquema fora do recorte, conteúdo protegido ou relação que exigiria interpretação não suportada. |
| `WSO2-COV-01` | Expõe que a amostra recebeu inventário e análise declarativa mínima, sem afirmar compreensão uniforme de middleware ou runtime. | `Analysis Coverage` por CAR e dimensão, `Explicit Gap`s e método do analisador. | Telemetria, comportamento em runtime e semântica não coberta pelo analisador. |

O resultado da amostra é comparável quando a lista de CARs, revisão/hash,
exclusões e autorização são iguais. Uma mudança na seleção cria nova revisão
do corpus e não deve ser apresentada como regressão do analisador.

### 4.3 ERPNext — inventário e escala

ERPNext permanece referência de inventário e escala. O protocolo não fixa uma
contagem artificial: a resposta esperada é a contagem reproduzível derivada do
inventário completo na revisão/hash registrada, por categoria, tipo e tamanho,
com locadores do manifesto. O recorte de pedido a faturamento serve para
delimitar perguntas e relações que podem ser marcadas como não suportadas;
semântica Python/Frappe profunda não é critério de aprovação do primeiro corte.

| Caso | Resposta de referência aceitável | Evidências esperadas | Lacunas obrigatórias |
| --- | --- | --- | --- |
| `ERP-INV-01` | Apresenta o inventário completo do recorte, suas categorias, volumes e exclusões para a revisão/hash efetivamente analisada. | Manifesto, lista de `Artifact`s, hashes e métricas de volume; `Analysis Snapshot`. | Qualquer fonte não autorizada, conteúdo inacessível e vínculos sem evidência. |
| `ERP-SCALE-01` | Compara primeira análise, repetição sem mudança e atualização localizada pelos mesmos contadores e métricas de recursos. | `run_id`, revisão/hash, artefatos descobertos/reutilizados/reprocessados, duração, memória, armazenamento e custo. | Não atribuir ganho a cache ou semântica não demonstrada; números locais não são SLA. |
| `ERP-FLOW-01` | Declara a presença, ausência ou não suporte de relações do recorte pedido a faturamento, sem preencher a lacuna com conhecimento geral sobre Frappe. | Artefatos e relações textuais/declarativas realmente encontrados, ou `Explicit Gap` com motivo. | Semântica Python/Frappe profunda, execução de negócio e `Observed Execution`. |

Uma resposta de ERPNext pode ser aprovada para inventário e escala mesmo quando
`ERP-FLOW-01` abstiver-se. Essa separação evita transformar volume de artefatos
ou páginas geradas em prova de compreensão semântica.

## 5. Camadas de avaliação e atribuição de falhas

A avaliação é piramidal. Cada camada produz um resultado identificável e não
substitui as camadas anteriores.

| Camada | Entrada e execução | Evidência de saída | Falhas que deve localizar |
| --- | --- | --- | --- |
| Fixtures determinísticas | Pequenos artefatos sintéticos e protegidos, sem provedor externo. | Entidades, relações, `Evidence`, `Provenance`, cobertura e lacunas esperadas. | Extração, normalização, contrato ou preservação de origem. |
| Contratos e integração local | Analisadores, persistência, reindexação e recuperação com provedores simulados. | Resultado estruturado, projeções reconstruíveis e pacote de evidências. | Integração local, projeção, autorização, reindexação ou recuperação. |
| Corpus de referência | Casos curados no Ticketmaster, CARs WSO2 e inventário/escala ERPNext, sempre por revisão/hash. | Respostas, locadores, métricas e limitações comparáveis. | Diferença entre profundidade, extração, recuperação e referência. |
| `live eval` | Subconjunto autorizado com provedor externo, somente por execução explícita e orçamento. | Modelo/configuração, insumos permitidos, saída, tokens, custo, latência e estado. | Variação do modelo, política, orçamento, recuperação ou geração. |

Para uma resposta incorreta ou incompleta, o relatório deve seguir esta ordem:

1. **Extração:** a entidade, relação, observação, cobertura ou lacuna esperada
   não foi produzida, ou seu locador não é válido;
2. **Recuperação:** a evidência foi produzida, mas não entrou nos candidatos ou
   no pacote limitado autorizado;
3. **Geração:** a evidência estava no pacote, mas a resposta omitiu, citou
   incorretamente, inferiu além do suporte ou não se absteve;
4. **Política/autorização:** o conteúdo existia, mas não podia ser transferido
   ou visualizado; isto é uma limitação explícita, não falha de extração.

Uma referência humana pode contestar essa atribuição. A contestação fica
registrada como `Curated knowledge` e não apaga o resultado observado.

## 6. Simulação padrão e `live eval` explícita

O modo padrão é `simulated`. Ele não chama o provedor externo, usa respostas e
similaridades determinísticas de fixture ou provedor simulado e permite
repetir analisadores, contratos, recuperação e validação do pacote sem
credencial. Uma saída simulada continua sendo `Generated knowledge`; somente
as evidências do corpus ou da fixture podem sustentar uma afirmação.

`live eval` exige uma execução explicitamente escolhida, autorização vigente,
orçamento suficiente e política de transferência aprovada para cada fonte. O
modelo recebe somente o pacote autorizado; não recebe acesso direto ao
diretório, ao manifesto com segredos ou a artefatos fora do escopo.

Cada execução registra apenas metadados necessários à reprodução e à auditoria:

| Grupo | Campos sem segredo |
| --- | --- |
| Identidade | `run_id`, caso/versão, corpus/revisão, fontes/hash, `Analysis Snapshot`, modo e resultado. |
| Engine | versão do engine, analisadores e fixtures; versão do contrato; ambiente e limitações conhecidas. |
| Modelo | provedor, identificador/model snapshot, configuração não secreta, versão do prompt/template e identificador da solicitação. |
| Consumo | tokens de entrada/saída quando fornecidos, custo estimado/real, latência por etapa, orçamento configurado, consumido e restante. |
| Segurança | decisão de política, autorização, quantidade de itens redigidos, `secret_present` booleano e motivo de bloqueio quando houver. |
| Resultado | estado de cobertura, falhas, evidências por identificador, métricas e justificativa de abstinência. |

Nunca são persistidos `secret key`, token, cabeçalho de autorização, conteúdo
proibido, prompt bruto com segredo ou resposta que a política de conteúdo não
permita reter. Quando o conteúdo da saída for sensível, registra-se somente o
identificador, um resumo autorizado ou um digest conforme a política. Tokens,
custo e latência de modo simulado são marcados como locais e não são
comparados diretamente com números de `live eval`.

O orçamento é aplicado antes de cada chamada externa e durante a suíte. Ao
atingir o limite, novas chamadas são bloqueadas, resultados já produzidos são
preservados e a execução termina como limitada por orçamento, não como sucesso
completo. O relatório não divulga a credencial usada.

## 7. Métricas, reanálises e limites de interpretação

Toda métrica recebe ambiente, recorte, revisão/hash, versão do caso, modo,
limitações e `run_id`. Os números são linha de base experimental; não são SLA,
capacidade comercial nem promessa de suporte uniforme.

### Extração

- precisão, cobertura e F1 de entidades por tipo quando houver referência;
- precisão, cobertura e F1 de relações por tipo e direção;
- validade dos locadores de `Evidence` e completude de `Provenance`;
- concordância do estado de `Analysis Coverage` e recuperação de `Explicit Gap`s
  esperadas;
- quantidade de artefatos descobertos, excluídos, não suportados e com falha.

### Recuperação

- `evidence_recall_at_k`: proporção de evidências esperadas presentes nos
  primeiros candidatos/pacote;
- `evidence_precision_at_k` e posição do primeiro suporte relevante;
- cobertura de identidade e proveniência por item recuperado;
- ruído, duplicação, travessia relacional indevida e vazamento de conteúdo não
  autorizado, cujo limite aceitável é zero;
- comportamento quando embeddings estão indisponíveis: resultados textuais,
  relacionais e evidências continuam utilizáveis e a limitação fica visível.

### Geração

- correção e cobertura das afirmações aceitáveis;
- precisão e cobertura das citações aos locadores recuperados;
- taxa de afirmações sem suporte e taxa de inferência apresentada como fato;
- abstinência apropriada para perguntas com `expected_gaps` materiais;
- preservação dos rótulos `Observed knowledge`, `Generated knowledge` e
  `Curated knowledge`, sem exigir igualdade textual entre respostas.

### Primeira análise, repetição e atualização localizada

| Fase | Medidas mínimas | Verificação adicional |
| --- | --- | --- |
| Primeira análise | Duração total e por etapa, pico de memória, volume persistido, itens descobertos/reprocessados, latência de consulta e custo/tokens externos. | Ambiente, revisão/hash, autorização e falhas são registrados. |
| Repetição sem mudança | As mesmas medidas e delta de itens reutilizados, reprocessados e alterados. | Resultado factual e evidências devem permanecer equivalentes; qualquer diferença vira regressão ou mudança explicada. |
| Atualização localizada | Itens alterados, dependentes reprocessados, tempo, memória, armazenamento, latência e custo. | Itens não afetados permanecem reutilizados e a invalidação de evidências é identificável. |

Uma comparação só é válida quando mantém corpus, revisão, caso, ambiente,
configuração e orçamento equivalentes. Mudança de modelo, política, seleção de
CARs, caminho de fonte ou conteúdo autorizado gera nova linha de base.

## 8. Microcorte comparável para a decisão posterior de stack

Antes de escolher runtime, bibliotecas ou persistência definitiva, cada
alternativa deve executar o mesmo microcorte lógico do manifesto, com as mesmas
revisões/hashes, exclusões, casos e autorização. O microcorte não decide a
stack; produz evidência comparável para uma decisão posterior.

As operações mínimas são:

1. descobrir os arquivos/membros autorizados e calcular seus hashes;
2. analisar uma amostra representativa Java/Quarkus do Ticketmaster;
3. abrir e analisar, sem executar conteúdo, os tipos de CAR WSO2 definidos no
   manifesto, incluindo referências XML observáveis;
4. inventariar o recorte ERPNext e representar texto/metadata no fallback
   genérico, sem exigir semântica Python/Frappe profunda;
5. transformar os resultados em entidades, relações, observações, evidências,
   cobertura, lacunas e unidades textuais no resultado mínimo comum;
6. escrever e ler em lote pela fronteira de persistência considerada e
   reconstruir as projeções textual, relacional e vetorial quando aplicável;
7. executar uma consulta híbrida limitada e medir a composição do pacote de
   evidências, sem acesso direto da IA à fonte;
8. repetir sem mudança e aplicar uma alteração localizada, registrando trabalho
   reutilizado e reprocessado;
9. executar sob concorrência limitada e em ambiente local compatível com
   Compose, sem converter essa compatibilidade em decisão de stack.

O relatório do microcorte deve comparar, no mínimo, duração por etapa, pico de
memória, volume persistido, concorrência efetiva, latência de descoberta e
consulta, itens reutilizados/reprocessados, falhas, custo externo e tokens.
Também registra maturidade e segurança das bibliotecas, interoperabilidade dos
analisadores e limites que impedem comparação equivalente. Benchmark sintético
isolado ou resultado de uma máquina de desenvolvimento não pode decidir sozinho
a stack nem ser comunicado como SLA.

## 9. Critérios de conclusão do protocolo

O protocolo está pronto para a próxima mudança de implementação quando:

- cada caso tem versão, corpus/revisão, pergunta, referência curada, autoria,
  afirmações aceitáveis, evidências esperadas, lacunas e aplicabilidade;
- o banco inicial cobre inventário, relações, fluxos, decisões, configurações,
  capacidades, erros, evidências e abstinência sem codificar respostas;
- Ticketmaster e WSO2 têm envelopes de referência verificáveis por locadores,
  e ERPNext tem referências de inventário/escala com profundidade semântica
  explicitamente limitada;
- as quatro camadas distinguem falha de extração, recuperação, geração e
  política;
- `simulated` é padrão, `live eval` é explícita e seu orçamento/telemetria não
  expõem credenciais ou conteúdo proibido;
- métricas e microcorte registram ambiente e limitações e permanecem separados
  de SLA e de decisões de produto.
