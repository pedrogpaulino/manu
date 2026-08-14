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

## Invariantes de colaboração e conhecimento

1. Especialistas podem revisar, corrigir e enriquecer conhecimento para os
   usuários autorizados da mesma `Organization`.
2. Toda síntese ou página gerada deve manter ligação rastreável com suas
   observações, claims, evidências e proveniência quando esses elementos
   existirem.
3. Conteúdo curado, suas revisões e seu contexto humano não são substituídos
   silenciosamente por uma análise automática. O resultado esperado é uma
   proposta, um alerta de desatualização ou um conflito para `Review`.
4. Evidências ocultas por política de visualização continuam distintas de
   claims sem evidência; autorização para ver conteúdo não altera sua origem.
5. Claims conflitantes permanecem identificáveis até que uma revisão humana
   ou outra decisão explícita explique como tratá-las.
6. Os tipos de conhecimento e os estados epistemológicos devem ser informados
   de modo que uma experiência não transforme uma hipótese em fato ou uma
   síntese em contribuição humana sem base.

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
- Como representar validade temporal, escopo e conflito entre claims sem
  reduzir incerteza a um único indicador de confiança?
- Quais estados e permissões de `Review` são suficientes para a primeira
  experiência de curadoria, preservando o histórico humano?
- Quando um `Flow` é uma visão técnica de um `Business Process` e quando deve
  permanecer uma descrição independente?

Enquanto essas questões não forem decididas, documentos e experiências devem
usar os termos acima com a definição mais específica que as evidências
permitirem, indicar ambiguidade e evitar apresentar uma convenção local como
verdade universal.
