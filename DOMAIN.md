# Domínio do Manu

## Propósito e limites

Este documento estabelece a linguagem inicial do `Knowledge Engine` e da
colaboração em torno da base de conhecimento viva do Manu. O vocabulário é
canônico para esta fundação: os nomes em inglês são preservados quando forem
usados em código, integrações ou decisões, e as definições são dadas em
português brasileiro.

O modelo abaixo é conceitual. Ele não escolhe um modelo físico de dados, não
prescreve tabelas, structs, chaves, cardinalidades, endpoints, pacotes ou uma
tecnologia de persistência. A forma de armazenar, indexar, versionar ou
distribuir esses conceitos será decidida somente quando uma capacidade
concreta exigir isso.

O `Knowledge Engine` transforma fontes técnicas e documentais em conhecimento
que pode ser examinado, relacionado, explicado e revisado. A base de
conhecimento viva é o resultado acumulado desse ciclo. Catálogo,
documentação/wiki, grafo, busca, chat, onboarding, análise de impacto e
orientação de investigação são experiências sobre essa base; nenhuma delas,
isoladamente, define o produto ou substitui o núcleo.

O escopo do produto, os públicos e o recorte do MVP pertencem a
[`PRODUCT.md`](PRODUCT.md). Fontes, processamento, implantação e fronteiras
arquiteturais pertencem a [`ARCHITECTURE.md`](ARCHITECTURE.md). Este documento
define os termos compartilhados por esses documentos, sem repetir suas
decisões.

## Mapa conceitual

O fluxo abaixo mostra como os conceitos se relacionam sem afirmar uma
implementação específica:

```text
Organization
    │ delimita autorização, políticas e conhecimento
    ├── Source
    │     └── Artifact ──► Observation ──► Evidence
    │                                      │ sustenta ou contesta
    │                                      ▼
    ├── Entity ◄──── Relationship ────► Entity
    │       ▲              │
    │       └──── Knowledge Claim ◄─────┘
    │                   │
    │                   ├── Provenance (origem, tempo e método)
    │                   └── Wiki Page ──► Revision
    │
    └── Review ──► Curation (criar, corrigir, enriquecer ou aprovar)
```

Uma observação pode resultar em evidência diretamente verificável, e uma
afirmação pode reunir evidências de fontes ou análises diferentes. Uma página
apresenta afirmações, evidências e contexto em linguagem legível. Revisões e
atos de curadoria preservam a evolução humana da página e do conhecimento;
uma nova análise não deve apagar silenciosamente esse histórico.

O contrato universal de compreensão acrescenta uma trilha conceitual à
mesma base, sem transformar o mapa em um desenho físico:

```text
Source ──► Specialized Analyzer ──► Analysis
                                      │
                                      ├── Analysis Coverage
                                      │       └── Explicit Gap
                                      └── Understanding Dimension

Competence Question ──► referência revisável ──► avaliação do conhecimento

Context Consumer ──► Context Request ──► Context Package
                                      ├── Entity / Relationship
                                      ├── Evidence / Provenance
                                      └── Coverage / Explicit Gap
```

Uma `Analysis` pode contribuir para apenas parte das dimensões e correlacionar
seus resultados com observações de outras fontes. A `Analysis Coverage` torna
visível o escopo tentado e o que permaneceu sem suporte; uma `Explicit Gap` não
é um espaço a ser preenchido por inferência.

`Organization` é a fronteira conceitual transversal: fontes, artefatos,
entidades, afirmações, páginas, permissões, políticas e atividades de revisão
pertencem a uma organização. Isso continua verdadeiro em uma instalação que
tenha somente uma organização e não antecipa como o isolamento será realizado.

## Glossário

### `Knowledge Engine`

Núcleo conceitual que descobre, interpreta, correlaciona e explica informações
provenientes das `Source`s. Ele transforma `Observation`s e seus suportes em
entidades, relações, claims e documentação que podem ser consultados e
curados. O engine não é sinônimo de um parser, de um grafo ou de um modelo de
IA; esses podem participar do fluxo sem substituir a linguagem do domínio.

### `Living knowledge base`

Base de conhecimento viva que reúne o conhecimento útil e rastreável sobre
uma `Organization` e pode evoluir com novas fontes, análises, revisões e
curadoria. Ela mantém observações, sínteses e contribuições humanas
distinguíveis, em vez de tratar a última execução automática como a única
versão da verdade.

### `Organization`

Fronteira de conhecimento, políticas e autorização de uma empresa cliente.
Uma organização reúne as fontes e o conhecimento autorizado para seus
usuários, além das políticas que determinam como seu conteúdo pode ser
processado, transferido e visualizado. Ela é uma unidade conceitual de
governança, não um compromisso com um modo específico de implantação ou
isolamento físico.

### `Source`

Origem configurada que o Manu pode analisar. Exemplos incluem um repositório
de código, um filesystem, uma API, um banco de dados, uma configuração ou um
conjunto de documentos existentes. Uma `Source` descreve a origem e o contexto
de acesso e processamento; não é o mesmo que o conteúdo concreto encontrado
nela (`Artifact`). Uma fonte também pode ter regras próprias sobre quais
metadados, trechos ou conteúdos completos podem ser processados.

### `Artifact`

Unidade concreta descoberta em uma `Source`, como um arquivo, documento,
especificação, resposta de API, definição de configuração ou outro item que
possa ser identificado e analisado. O artefato é o objeto observado, não a
interpretação que o analisador faz dele. Sua identidade, versão e momento de
coleta podem ser relevantes para saber se uma observação ainda representa o
ambiente.

### `Observation`

Resultado produzido por um analisador sobre um `Artifact`. Uma observação
registra o que foi encontrado por um método em determinado momento, incluindo
sinais, referências ou extrações que possam ser verificadas. Ela pode ser
incompleta, estar desatualizada ou discordar de outra observação. Uma
`Observation` não vira automaticamente uma verdade, uma `Knowledge Claim` ou
uma decisão humana: sua interpretação deve permanecer rastreável à fonte,
ao método e às evidências.

### `Specialized Analyzer`

Analisador especializado é uma responsabilidade conceitual que interpreta uma
`Source` ou seus `Artifact`s com a semântica apropriada ao tipo de fonte. Ele
produz contribuições para o contrato universal de compreensão, mas não precisa
cobrir todas as dimensões nem possuir a mesma profundidade de outro analisador.
O termo descreve responsabilidade e significado, não protocolo, plugin,
linguagem, processo ou tecnologia de implementação.

### `Analysis`

`Analysis` é uma aplicação delimitada de um ou mais `Specialized Analyzer`s a
uma fonte, seus artefatos e o contexto disponível. Seu resultado pode conter
`Observation`s, `Knowledge Claim`s, `Evidence`, `Provenance`, relações,
`Analysis Coverage` e `Explicit Gap`s. Uma análise descreve o que foi tentado
e sustentado naquele recorte; não equivale à compreensão completa da fonte nem
à ocorrência de um comportamento em runtime.

### `Understanding Contract`

Contrato universal de compreensão é o vocabulário conceitual comum pelo qual
analisadores especializados descrevem suas contribuições para a base de
conhecimento viva. O contrato permite correlacionar resultados de fontes
distintas, preservando origem, método, suporte, temporalidade, cobertura e
lacunas. Ele define significado e qualificadores, não formato de serialização,
modelo físico de dados ou mecanismo de integração.

### `Understanding Dimension`

Dimensão de compreensão é uma área semântica que uma análise pode tentar
descrever. As dimensões iniciais são: paisagem, inventário e estrutura;
entidades e relações; fluxos e dependências; decisões, condições e origens dos
dados; variações por configuração, ambiente ou feature flag; capacidades e
formas de acesso; erros, criação, propagação e caminhos possíveis; evolução
entre revisões, releases, configurações e implantações; correspondência e
divergência documental; e evidências, proveniência, incerteza e lacunas.

Uma dimensão orienta a correlação e a avaliação, mas não é uma promessa de que
toda `Source` poderá fornecê-la ou de que todos os analisadores a produzirão no
mesmo nível de detalhe.

### `Analysis Coverage`

Cobertura da análise é a declaração contextual das dimensões e escopos que um
`Specialized Analyzer` tentou examinar, dos resultados produzidos e do suporte
que permaneceu limitado. Para cada dimensão, a cobertura deve distinguir, no
mínimo, resultado produzido, resultado incompleto, não suportado, não
aplicável e falha. Esses estados descrevem o alcance daquela análise; não são
um selo binário de compatibilidade, uma medida universal de qualidade ou uma
pontuação única de confiança.

Cobertura efetiva continua válida quando uma dimensão falha: os resultados das
demais dimensões não são descartados, e a falha permanece visível junto ao seu
contexto.

### `Explicit Gap`

Lacuna explícita é uma ausência material de conhecimento ou suporte que a
análise reconhece e apresenta como tal. Pode resultar de dimensão não
suportada ou não aplicável, falha parcial, escopo não analisado, evidência
inacessível, conflito não resolvido, contexto temporal desconhecido ou falta de
telemetria. Lacuna explícita não significa que o fato esteja ausente no
ambiente, e ausência de observação não é evidência de ausência. Ela impede que
uma inferência seja usada para ocultar o que as fontes não sustentam.

### `Competence Question`

Pergunta de competência é uma pergunta representativa, versionada e revisável
que expressa uma necessidade de um público autorizado e testa se o
`Knowledge Engine` compreendeu um recorte da base. Ela relaciona o contexto e a
revisão das fontes, uma resposta de referência curável, evidências esperadas
quando existirem e os critérios pelos quais se avaliam correção, cobertura,
rastreabilidade, atualidade, incerteza e abstinência apropriada. Não é apenas
uma pergunta livre de usuário nem se prova pelo volume de páginas ou
documentação gerada.

### `Entity`

Coisa canônica que o Manu reconhece ou acompanha no ambiente da organização e
que pode participar de relações ou afirmações. Uma entidade pode representar,
por exemplo, um `System`, uma `Application`, um `Service`, um `Component`, um
`Business Process`, uma equipe, uma pessoa, um recurso ou outro elemento
relevante para o conhecimento. A entidade é uma identidade conceitual que
reúne referências possivelmente encontradas em fontes distintas; regras de
deduplicação e identificação definitiva ainda não são fixadas aqui.

### `System Graph`

Representação conceitual e navegável de `Entity`s e `Relationship`s do
ambiente, apoiada por `Knowledge Claim`s, `Evidence` e `Provenance`. O grafo
ajuda a explorar contexto, dependências e caminhos de impacto, mas é uma
visão da base de conhecimento viva, não a base inteira nem o produto isolado.

### `Relationship`

Associação com significado entre duas ou mais `Entity`s. Ela pode expressar,
por exemplo, que uma aplicação chama um serviço, que um componente pertence a
uma aplicação, que um processo usa um sistema ou que uma entidade depende de
outra. O tipo, direção, temporalidade e força da relação devem ser sustentados
por evidências quando essa relação for apresentada como conhecimento. Uma
`Relationship` é uma representação do vínculo; não implica, sozinha,
causalidade, propriedade organizacional ou certeza absoluta.

### `Knowledge Claim`

Afirmação explícita sobre o ambiente, seus elementos ou suas relações. Uma
claim pode dizer, por exemplo, que uma `Application` depende de um `Service`
ou que um `Business Process` passa por determinada etapa. Ela deve poder
apontar para evidências e proveniência, registrar seu período de validade e
indicar quando está apoiada, contestada, desatualizada ou aguardando revisão.
Uma claim é uma unidade de conhecimento comunicável, não um sinônimo de
observação nem uma garantia de que a afirmação seja verdadeira.

Claims conflitantes devem permanecer distinguíveis, com suas evidências,
proveniência, temporalidade e estado de revisão. O sistema não deve fabricar
uma certeza única apenas porque duas fontes discordam.

### `Evidence`

Material verificável que apoia ou contesta uma `Knowledge Claim`. Pode ser um
trecho de documento, símbolo de código, definição, resposta de API, resultado
de análise, referência a um `Artifact` ou registro de uma revisão, conforme o
que for permitido pelas políticas de conteúdo e pela autorização do usuário.
Evidência responde “em que podemos nos apoiar?”; ela não substitui a
afirmação que está sendo avaliada. A disponibilidade para visualização pode
ser menor que a existência da evidência, sem eliminar sua proveniência.

### `Provenance`

Rastro da origem e da transformação de um item de conhecimento. A proveniência
explica de onde veio, quando foi obtido, por qual método foi produzido,
transformado ou sintetizado e, quando aplicável, quem o revisou ou curou.
Enquanto `Evidence` é o suporte verificável de uma claim, `Provenance` explica
a história desse suporte e da própria afirmação, página ou revisão. Manter as
duas noções separadas permite expor incerteza, temporalidade e conflito sem
perder a origem.

### `Wiki Page`

Unidade publicável de documentação legível por pessoas, construída sobre a
base de conhecimento. Uma página pode reunir observações, claims, evidências,
explicações geradas e contribuições curadas, sempre que o leitor autorizado
puder distinguir essas origens. Ela é uma experiência de documentação sobre o
conhecimento, não a fonte original nem o grafo inteiro. Páginas geradas devem
ser editáveis e referenciar o conhecimento que as sustenta.

### `Revision`

Evolução identificável de uma `Wiki Page` ou de conteúdo curado. Uma revisão
preserva o que mudou, o contexto da mudança e sua autoria ou origem, de modo
que uma página possa ser comparada e recuperada ao longo do tempo. Nova
análise pode propor uma revisão ou sinalizar desatualização e conflito, mas
não deve sobrescrever silenciosamente uma revisão produzida por uma pessoa.

### `Review`

Avaliação realizada por um usuário autorizado, especialmente um especialista,
sobre uma claim, evidência, página, revisão ou resultado de análise. A revisão
pode confirmar, contestar, corrigir, pedir mais evidência ou indicar que o
item precisa ser reavaliado. Ela registra julgamento e contexto; não significa
por si só que o conteúdo foi alterado.

### `Curation`

Atividade intencional de criação, correção, enriquecimento, aprovação,
organização ou retirada de destaque de conhecimento por uma pessoa autorizada.
A curadoria transforma uma avaliação ou conhecimento próprio do especialista
em conteúdo que pode ser reutilizado pelos usuários autorizados da
organização. Ela preserva autoria, justificativa e relação com as evidências,
e pode coexistir com observações e sínteses que apontem em outra direção.
`Curation` é o ato e o ciclo de melhoria; `Review` é a avaliação que pode
desencadeá-lo. Conhecimento curado não é infalível, mas não pode ser perdido
por uma nova análise automática.

### `Capability`

`Capability` é algo que o ambiente analisado oferece ou permite realizar para
algum consumidor, pessoa ou processo. Ela descreve uma possibilidade efetiva
do ambiente, como consultar um relatório, executar uma operação ou obter uma
saída sob certas condições. Uma `Capability` pode ser relacionada a entidades,
acesso, entradas, saídas, evidências e documentação conhecidas; não é o mesmo
que a explicação que o Manu produz sobre ela.

Um relatório que já existe no ambiente é tratado como `Capability` ou recurso
disponível do ambiente, conforme a evidência permitir. A análise deve
relacioná-lo ao modo de acesso, às entradas e saídas e à documentação
conhecida, sem reclassificá-lo como produto do Manu apenas por tê-lo
descoberto.

### `Knowledge Product`

`Knowledge Product` é uma composição consumível de conhecimento produzida,
organizada ou publicada pelo Manu para apoiar entendimento, consulta,
investigação ou mudança. Pode ser uma página, explicação, mapa, catálogo ou
relatório de impacto e deve apontar para as `Capability`s, relações, claims,
evidências e lacunas utilizadas, quando existirem. Seu papel é apresentar ou
aplicar conhecimento da base; ele não se torna uma capacidade do ambiente que
está sendo analisado.

Um relatório de impacto gerado pelo Manu, por exemplo, é um `Knowledge Product`
mesmo quando descreve relatórios e outras capacidades existentes no ambiente.
Os dois usos da palavra “relatório” permanecem distinguíveis pela origem,
proveniência e responsabilidade: o primeiro é recurso do ambiente, o segundo é
resultado produzido pelo Manu.

### `Context Consumer`

Consumidor de contexto é uma pessoa, experiência, API ou agente autorizado que
recebe uma representação limitada e verificável do conhecimento para realizar
uma tarefa. O consumidor não recebe, por esse papel, acesso direto à `Source`,
à persistência ou a operações administrativas. Um agente pode consumir o
pacote sem um `Generator`; quando houver geração, o resultado continua sendo
`Generated knowledge`.

### `Context Request`

Solicitação de contexto é o pedido explícito de um `Context Consumer` para uma
intenção de entendimento, como localizar uma entidade, explorar uma relação,
investigar impacto possível ou inspecionar uma `Evidence`. Ela identifica a
`Organization`, a `Source`, o `Analysis Snapshot` ou uma resolução permitida,
além dos limites positivos e das políticas aplicáveis. Não é uma mensagem de
um protocolo específico nem autoriza ampliar o escopo solicitado.

### `Context Package`

Pacote de contexto é uma composição versionada, autorizada e limitada do
conhecimento da base para atender a um `Context Request`. Ele pode reunir
`Entity`s, `Relationship`s, `Knowledge Claim`s e unidades de `Evidence` com
seus locadores e `Provenance`, além de cobertura, `Explicit Gap`s,
degradações, revisão do snapshot, estimativa de custo e indicação de
continuação. O pacote mantém distinguíveis conhecimento observado, gerado e
curado; fatos e relações produzidos tecnicamente por derivação permanecem
identificáveis por sua linhagem, sem constituir um estado epistemológico
adicional. O pacote não é a `Source`, não substitui a evidência original e não
deve apresentar uma relação sem o suporte necessário.

### `Context Item`

Item de contexto é uma unidade selecionada para um `Context Package`, como uma
entidade, relação, claim ou evidência com o suporte e o locator permitidos.
Cada item conserva seu escopo, origem, revisão e condição de disponibilidade.
Um item excluído, truncado ou redigido não pode ser tratado pelo consumidor
como se tivesse sido examinado integralmente.

### `Context Continuation`

Continuação de contexto é uma referência opaca para obter a próxima parte
determinística de um pacote que excedeu seus limites. Ela permanece vinculada
ao mesmo consumidor autorizado, `Organization`, `Source`, snapshot, intenção,
política e ordenação; não concede autorização nova e deixa de ser válida quando
esses contextos forem incompatíveis.

## Núcleo, experiências e colaboração

O `Knowledge Engine` é o núcleo que descobre, interpreta e relaciona
informações. A base de conhecimento viva é o conjunto evolutivo de entidades,
relações, claims, evidências, proveniência e páginas que pode ser atualizado,
questionado e curado. O ciclo conceitual é:

```text
descoberta → observação → correlação → claim/evidência
     → síntese documental → review → curation/publicação
     ▲                                      │
     └──────── nova análise, conflito ou desatualização ────────┘
```

As experiências consomem o mesmo núcleo:

- `Catalog` organiza e permite encontrar entidades, fontes, páginas e outros
  itens relevantes;
- `Documentation/Wiki` apresenta e edita `Wiki Page`s e suas `Revision`s;
- `Graph` explora `Entity`s e `Relationship`s sustentadas por claims e
  evidências;
- busca e chat recuperam e explicam conhecimento, mostrando a proveniência
  apropriada;
- contexto para agentes organiza `Context Request`s em `Context Package`s para
  consumidores autorizados, com evidências, locadores, cobertura e lacunas;
- onboarding, análise de impacto e orientação de investigação aplicam o
  conhecimento a uma tarefa, sem transformá-la no significado inteiro do
  Manu.

Essas experiências não criam vocabulários concorrentes. Quando uma tela,
resposta ou página precisar nomear algo do ambiente, deve reutilizar os termos
canônicos deste documento e apontar para o conhecimento que sustenta a
apresentação.

## Tipos de conhecimento

Os tipos abaixo descrevem a origem epistemológica do conhecimento, não um
formato de armazenamento, nível de confiança ou estágio obrigatório do
produto. Um mesmo artefato documental pode conter itens de origens distintas;
essa origem deve continuar visível.

### `Observed knowledge`

Conhecimento observado é aquilo que um analisador encontrou diretamente em
uma `Source` ou em um `Artifact`, dentro do alcance e do método usados. Ele
deve preservar `Evidence` e `Provenance` suficientes para permitir a
verificação. É observacional, não necessariamente completo, atual ou correto
em todos os contextos; ausência de observação não prova ausência no ambiente.

### `Generated knowledge`

Conhecimento gerado é uma síntese, explicação, correlação ou inferência
produzida pelo `Knowledge Engine`, por regras, por um agente ou por outro
método de transformação a partir de observações, claims e evidências. Deve
indicar os insumos e o método que lhe deram origem e não deve ser apresentado
como contribuição humana aprovada por padrão. Uma geração pode ser útil mesmo
quando precisa de `Review` ou contém uma hipótese a validar.

### `Curated knowledge`

Conhecimento curado é criado, corrigido, enriquecido ou aprovado por um
especialista ou outro usuário autorizado da `Organization`. Deve preservar
autoria, contexto, justificativa, evidências relacionadas e evolução por
`Revision` quando aplicável. Curadoria pode resolver uma ambiguidade,
acrescentar contexto que nenhuma fonte contém ou contestar uma síntese
automática; ela não apaga a existência de observações discordantes.

### Origem e estado não são a mesma coisa

`Observed knowledge`, `Generated knowledge` e `Curated knowledge` respondem a
“como este conhecimento foi produzido?”. Já estados como os seguintes
respondem a “qual é sua posição no trabalho do projeto?” e podem aparecer em
qualquer origem:

- `Current constraint`: condição vigente que limita o trabalho agora;
- `Accepted decision`: escolha adotada, cuja justificativa deve ser rastreável
  quando houver uma decisão registrada;
- `Hypothesis`: suposição explicitamente sujeita a validação;
- `Future option`: direção possível, sem compromisso de entrega ou adoção.

Não se deve usar “gerado” como sinônimo de “hipótese”, nem “curado” como
sinônimo de “decisão aceita”. Uma claim curada pode continuar sendo uma
hipótese, e uma observação pode documentar uma restrição atual sem ter sido
curada por uma pessoa.

## Contextos temporais e de execução

Conhecimento que depende de versão, ambiente ou momento deve manter os
contextos que as fontes e a análise conseguirem sustentar. Eles são
qualificadores relacionados, não um único campo genérico de “versão” e não
um modelo físico de armazenamento.

| Contexto | Definição conceitual | Distinção principal |
| --- | --- | --- |
| `Source Revision` | Revisão identificável da `Source` ou do `Artifact` que foi observada, como uma versão de código, documento ou definição de origem. | Indica o que estava na fonte; não afirma qual build foi implantado nem que foi executado. |
| `Analysis Snapshot` | Recorte temporal e metodológico no qual uma `Analysis` examinou fontes, artefatos e contextos disponíveis. | Indica o que a análise tentou observar e quando; não substitui a revisão da fonte ou o estado do ambiente. |
| `Environment` | Contexto operacional reconhecível em que uma aplicação, serviço ou processo pode existir, como desenvolvimento, teste ou produção. | Nomeia o contexto de operação; não é, por si só, uma implantação nem prova de execução. |
| `Release` | Identidade de uma versão distribuível ou comunicada de uma aplicação, serviço ou produto do ambiente. | Pode relacionar fonte, build e implantação, mas não é sinônimo de nenhum deles. |
| `Build Artifact` | Artefato concreto produzido por uma transformação de build, pronto para ser distribuído ou implantado. | Pode ter vínculo conhecido com uma `Source Revision` e um `Release`; não é a implantação nem a execução. |
| `Deployment` | Estado ou ato de disponibilizar um `Build Artifact` em um `Environment`, com seu contexto temporal e demais vínculos conhecidos. | Indica disponibilidade pretendida ou realizada; não prova que houve uma `Observed Execution`. |
| `Configuration State` | Configuração efetiva ou pretendida para um `Environment` ou `Deployment`, incluindo regras de seleção e variações por configuração ou feature flag quando conhecidas. | Pode alterar o comportamento possível sem alterar a `Source Revision`; não deve ser confundida com o código ou com um segredo não observado. |
| `Documentation Revision` | Revisão identificável do conteúdo documental usado ou produzido para explicar o ambiente, incluindo documentos existentes e páginas mantidas na base. `Revision` é a forma específica usada para a evolução de uma `Wiki Page` ou de conteúdo curado; `Documentation Revision` qualifica de modo mais amplo a revisão documental comparada ao ambiente. | Pode estar defasada em relação à fonte; não é a mesma coisa que `Source Revision` ou uma revisão de código. |

Uma análise registra somente os vínculos temporais e de execução que as
evidências sustentam. Qualquer contexto ou ligação pode estar ausente,
desconhecido ou não aplicável, e essa condição deve permanecer explícita;
vínculo ausente não significa que a relação seja falsa. Uma comparação deve
declarar quais contextos possui, quais desconhece e quais não se aplicam, sem
atribuir uma diferença a código, configuração, build, implantação ou
documentação quando a causa não puder ser sustentada.

## Distinções de elementos do ambiente

Os termos abaixo são categorias conceituais de `Entity`. Eles não definem
limites de implantação, pacotes ou ownership técnico por si só. A distinção
principal é o que cada elemento representa e qual pergunta ele ajuda a
responder.

| Termo | Definição inicial | Não confundir com |
| --- | --- | --- |
| `System` | Contexto amplo e reconhecível do ambiente, formado por partes técnicas, pessoas, dados ou processos que cooperam para algum propósito. Pode incluir aplicações próprias e dependências externas. | Um único processo, repositório ou serviço; `System` é a fronteira de contexto, não necessariamente um artefato implantável. |
| `Application` | Unidade de software reconhecível por sua finalidade e pelos usuários ou consumidores que atende. Pode ser composta por serviços e componentes e pode participar de um ou mais sistemas. | Uma página ou um componente interno; também não implica que toda aplicação seja um único pacote ou deploy. |
| `Service` | Capacidade oferecida a consumidores por um contrato ou interface compreensível, técnica ou organizacional. O foco está no que é oferecido e em como pode ser consumido, não em como foi implementado. | Um componente que apenas implementa a capacidade; nem todo serviço precisa ser uma aplicação independente. |
| `Component` | Parte identificável de uma aplicação, serviço ou sistema que exerce uma responsabilidade e coopera com outras partes. Pode ser uma unidade lógica ou uma parte técnica descoberta, sem que isso determine seu empacotamento. | Uma capacidade pública por si só; a fronteira de um componente é menor e orientada à composição. |
| `Business Process` | Conjunto de atividades, regras, decisões e participantes que atravessa um contexto para produzir um resultado de negócio. Pode usar várias aplicações e serviços e não é reduzido às chamadas técnicas que o suportam. | Um fluxo de dados ou de requisições; o processo é orientado a objetivo, resultado e contexto de negócio. |
| `Flow` | Percurso ou sequência de eventos, dados, mensagens, controle ou atividades entre entidades ou etapas. Pode descrever um comportamento técnico ou realizar parte de um processo de negócio. | Um `Business Process` completo; um flow mostra movimento ou sequência, enquanto o processo explica finalidade, regras e resultado. |

Em termos práticos, uma `Application` pode oferecer um `Service`, ser formada
por `Component`s e participar de um `System`; um `Business Process` pode
atravessar várias aplicações por meio de um ou mais `Flow`s. Essas relações
são exemplos de modelagem, não uma taxonomia fechada nem uma regra de
hierarquia obrigatória.

### `Possible Flow`

`Possible Flow` é a qualificação de um `Flow` reconstruído a partir de código,
contratos, configurações, documentação ou outras fontes que sustentem um
caminho que pode ocorrer. Ele descreve possibilidade sob as condições
analisadas, incluindo ramificações e dependências conhecidas; não descreve que
o caminho ocorreu. Sem telemetria ou outro registro operacional que sustente a
ocorrência, um `Possible Flow` não pode ser apresentado como execução em
runtime.

### `Observed Execution`

`Observed Execution` é a ocorrência de um `Flow` sustentada por evidência
operacional autorizada, em um contexto temporal e ambiental conhecido ou
explicitamente desconhecido. A evidência deve indicar o que foi observado e
seu escopo; ela não transforma automaticamente a execução em explicação de
causa, decisão de negócio ou ocorrência de todas as etapas de um processo.
Quando não há telemetria ou registro equivalente, a ocorrência permanece
desconhecida e o Manu deve conservar o `Possible Flow` como possibilidade, sem
afirmar que ele aconteceu em runtime.

### Relação entre `Flow` e `Business Process`

`Flow` é o conceito geral de percurso ou sequência. Um `Possible Flow` é uma
visão de comportamento que pode apoiar parte de um `Business Process`, e um
`Observed Execution` fornece evidência de uma ocorrência desse percurso. Já o
`Business Process` é orientado a objetivo, regras, participantes e resultado
de negócio; pode atravessar várias aplicações e ser apoiado por vários flows.
Uma documentação ou curadoria pode relacionar os conceitos, mas a relação
deve preservar se veio de fonte técnica, observação operacional ou
interpretação de negócio. Observar um flow não prova, por si só, a intenção,
as regras ou a conclusão de um `Business Process`.

## Invariantes de colaboração e conhecimento

As dimensões abaixo são qualificadores independentes de uma afirmação ou
produto de conhecimento: origem (como foi produzido), suporte (em que se
apoia e se é contestado), realidade comportamental (que tipo de fluxo ou
processo descreve), temporalidade (em quais contextos vale) e lacunas (o que
permanece sem cobertura ou conhecimento). Nenhuma dimensão deve ser deduzida
automaticamente de outra.

1. Especialistas podem revisar, corrigir e enriquecer conhecimento para os
   usuários autorizados da mesma `Organization`.
2. `Observed knowledge`, `Generated knowledge` e `Curated knowledge` registram
   a origem da produção. Uma origem não pode ser inferida de cobertura,
   evidência, temporalidade, estado de contestação ou realidade comportamental.
3. Toda afirmação, página, relação, explicação ou `Knowledge Product` que
   possa orientar entendimento deve manter `Evidence` e `Provenance` quando
   existirem, distinguir suporte de contestação e declarar quando o suporte é
   insuficiente.
4. Cada `Analysis` deve declarar sua `Analysis Coverage`, incluindo dimensões
   e escopos tentados e estados produzidos, incompletos, não suportados, não
   aplicáveis ou em falha. Resultados de dimensões concluídas continuam
   utilizáveis quando outra dimensão falhar.
5. `Explicit Gap`s devem permanecer visíveis para ausência de cobertura,
   contexto, telemetria, evidência ou resolução. Uma lacuna não deve ser
   preenchida por inferência nem ser tratada como prova de que o fato não
   existe.
6. `Flow`, `Possible Flow`, `Observed Execution` e `Business Process` mantêm
   realidades comportamentais distintas. Sem telemetria ou registro
   operacional equivalente, o Manu não pode afirmar que um `Possible Flow`
   ocorreu em runtime; uma interpretação de `Business Process` também não é
   observação automática do código.
7. Contextos como `Source Revision`, `Analysis Snapshot`, `Environment`,
   `Release`, `Build Artifact`, `Deployment`, `Configuration State` e
   `Documentation Revision` permanecem separados. Vínculos ausentes,
   desconhecidos ou não aplicáveis são informados, e comparações não atribuem
   uma causa sem suporte contextual.
8. Conteúdo curado, suas revisões e seu contexto humano não são substituídos
   silenciosamente por uma análise automática. O resultado esperado é uma
   proposta, um alerta de desatualização ou um conflito para `Review`.
9. Evidências ocultas por política de visualização continuam distintas de
   claims sem evidência; autorização para ver conteúdo não altera sua origem,
   seu suporte ou sua proveniência.
10. Claims conflitantes permanecem identificáveis até que uma revisão humana
    ou outra decisão explícita explique como tratá-las.
11. Os qualificadores não são condensados em uma pontuação única de confiança.
    Qualquer resumo futuro deve preservar os fatores de origem, suporte,
    comportamento, contexto temporal e lacuna para inspeção.
12. Perguntas de competência e suas referências são versionadas e revisáveis;
    uma resposta é avaliada por correção, cobertura, rastreabilidade,
    atualidade, incerteza e abstinência apropriada, não pelo volume de
    documentação gerada.
13. Um `Context Request` deve declarar organização, fonte, snapshot, intenção e
    limites; ausência ou ambiguidade de escopo não é resolvida combinando
    revisões silenciosamente.
14. Um `Context Package` é uma representação limitada e autorizada para um
    `Context Consumer`; ele preserva evidências, proveniência, cobertura,
    lacunas e as origens epistemológicas dos itens que apresenta.
15. Cada `Context Item` deve permanecer sujeito à autorização e à política
    aplicáveis no momento do consumo. Redaction ou indisponibilidade não pode
    ser convertida em fato ou suporte implícito.
16. Uma `Context Continuation` não amplia escopo nem autorização e não troca
    silenciosamente o snapshot solicitado por uma revisão posterior.

## Questões abertas

As perguntas a seguir são deliberadamente abertas. Elas serão resolvidas com
exemplos reais, necessidades do MVP e decisões aceitas; não devem ser
preenchidas por suposições escondidas neste glossário.

- Qual é o critério operacional para separar `System`, `Application` e
  `Service` em um ambiente legado onde os limites são sobrepostos?
- Um `Service` deve abranger sempre uma capacidade técnica, ou o vocabulário
  precisará distinguir explicitamente serviços de negócio de serviços de
  software?
- Em que nível de granularidade um módulo, job, biblioteca ou pipeline passa a
  ser um `Component` relevante para o conhecimento e não apenas um detalhe da
  fonte?
- Quais evidências mínimas permitem afirmar que dois nomes encontrados em
  fontes diferentes representam a mesma `Entity`?
- Como detalhar validade, escopo e conflito entre claims nos contextos
  temporais disponíveis sem reduzir incerteza a um único indicador de
  confiança?
- Qual granularidade de `Analysis Coverage` é útil para cada experiência sem
  ocultar dimensões incompletas, não suportadas, não aplicáveis ou em falha?
- Quais evidências operacionais são suficientes para qualificar uma ocorrência
  como `Observed Execution`, e como relacioná-la a um `Business Process` sem
  confundir comportamento observado com interpretação de negócio?
- Como registrar e revisar `Competence Question`s, referências e critérios
  quando especialistas discordarem sobre a resposta esperada?
- Quais estados e permissões de `Review` são suficientes para a primeira
  experiência de curadoria, preservando o histórico humano?
- Como representar vínculos ausentes ou desconhecidos entre `Source Revision`,
  `Build Artifact`, `Deployment`, `Configuration State` e
  `Documentation Revision` sem sugerir uma causa que não foi observada?

Enquanto essas questões não forem decididas, documentos e experiências devem
usar os termos acima com a definição mais específica que as evidências
permitirem, indicar ambiguidade e evitar apresentar uma convenção local como
verdade universal. Uma futura forma de resumir cobertura ou suporte não deve
substituir a inspeção desses qualificadores por uma pontuação de confiança.
