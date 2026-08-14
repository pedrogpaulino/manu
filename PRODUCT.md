# Produto — Manu

## Resumo

O Manu é uma plataforma que transforma fontes técnicas e documentais de
ambientes empresariais grandes e legados em uma base de conhecimento viva.
Seu núcleo é o **Knowledge Engine**: ele descobre, correlaciona e explica o
que existe nas fontes, mantendo evidências e proveniência para que o
conhecimento possa ser consultado, revisado e enriquecido ao longo do tempo.

Catálogo, documentação/wiki, grafo, busca, chat, onboarding, análise de
impacto e orientação de investigação são experiências construídas sobre esse
núcleo. Nenhuma delas, isoladamente, define o produto.

## Como ler a visão

As afirmações desta página usam quatro estados para não confundir o que já
limita o trabalho com o que ainda precisa ser aprendido:

| Estado | Significado |
| --- | --- |
| **Restrição atual** (*Current constraint*) | Condição que limita o recorte inicial, mesmo que ainda não seja uma capacidade do produto. |
| **Decisão aceita** (*Accepted decision*) | Escolha vigente para orientar produto e colaboração; não é uma promessa de que tudo já esteja implementado. |
| **Hipótese** (*Hypothesis*) | Suposição que precisa ser testada com aplicações, usuários ou compradores reais. |
| **Opção futura** (*Future option*) | Direção possível, sem compromisso de entrega ou de desenho definitivo. |

Os sinais e métricas do MVP são instrumentos de aprendizado. Seus limiares
podem ser ajustados quando houver uma linha de base real; eles não são SLAs ou
promessas comerciais.

## Problema

Em empresas grandes, especialmente nas que acumulam sistemas legados, o
conhecimento necessário para operar e mudar uma aplicação fica fragmentado
entre código, arquivos, APIs, bancos de dados, configurações, documentos,
diagramas e a memória de poucas pessoas. Esses registros usam linguagens e
nomenclaturas diferentes, envelhecem em ritmos diferentes e raramente
explicam como uma decisão ou dependência se sustenta em evidências.

Essa fragmentação torna caro e arriscado:

- descobrir quais aplicações, serviços, componentes e processos se relacionam;
- encontrar a documentação que ainda corresponde ao ambiente atual;
- entender o impacto de uma mudança sem depender de uma pessoa específica;
- integrar alguém novo à sustentação, à arquitetura ou ao negócio;
- orientar uma investigação sem transformar uma inferência em fato;
- preservar o conhecimento humano enquanto novas análises são executadas.

O problema não é apenas falta de busca. É a ausência de um contexto comum,
verificável e atualizável que conecte fontes diferentes sem apagar as
incertezas e as decisões humanas.

## Identidade e núcleo do produto

### Decisão aceita: Knowledge Engine e base de conhecimento viva são o centro

O Manu deve ser entendido nesta sequência:

```text
Fontes técnicas e documentais
  código · arquivos · APIs · bancos · configurações · documentos
                              │
                              ▼
                      Knowledge Engine
           descoberta · correlação · evidências · proveniência
                              │
                              ▼
                  Base de conhecimento viva
       conhecimento observado · gerado · curado e revisável
                              │
                              ▼
              experiências para cada público autorizado
```

O Knowledge Engine não é somente um parser, um grafo ou um modelo de IA. Ele
organiza observações e relações em conhecimento que pode ser usado e
questionado. A base é **viva** porque novas fontes e análises podem trazer
atualizações, conflitos ou lacunas, enquanto revisões humanas e seu histórico
continuam preservados.

### Estados do conhecimento

Toda explicação ou relação relevante deve deixar claro de onde veio e qual é
seu estado epistemológico:

- **Observed knowledge (conhecimento observado):** resultado encontrado
  diretamente por um analisador em uma fonte, com referência ao artefato e ao
  momento da observação.
- **Generated knowledge (conhecimento gerado):** síntese, resumo, explicação
  ou inferência produzida a partir de observações e evidências. Deve ser
  apresentada como gerada, e não como fato independente da fonte.
- **Curated knowledge (conhecimento curado):** conteúdo criado, corrigido,
  enriquecido ou aprovado por um especialista. A autoria, a revisão e a
  evolução desse conteúdo devem ser preservadas.

Quando uma nova análise divergir de conteúdo curado, a experiência deve
sinalizar desatualização ou conflito e propor revisão. Ela não deve
sobrescrever silenciosamente a contribuição humana.

### Princípios de experiência

As experiências do Manu devem:

1. começar pelo contexto compartilhado da base, e não por uma tela isolada;
2. mostrar evidência, proveniência, temporalidade e grau de incerteza quando
   uma afirmação puder afetar uma decisão;
3. permitir que especialistas revisem, corrijam e enriqueçam o conhecimento
   para todos os usuários autorizados da organização;
4. tratar busca e chat como formas de explorar conhecimento sustentado, não
   como autorização para inventar respostas;
5. respeitar a fronteira da organização e as permissões de visualização do
   conteúdo disponível.

## Públicos iniciais e comprador

### Públicos que usam o produto

| Público | Necessidade inicial | Resultado que o Manu deve apoiar |
| --- | --- | --- |
| **Sustentação** | Entender uma aplicação desconhecida, localizar dependências e orientar uma investigação ou mudança. | Menos tempo procurando contexto e mais capacidade de agir com evidências, mesmo quando o especialista original não está disponível. |
| **Arquitetura** | Construir e revisar a visão de sistemas legados, relações e impactos entre aplicações. | Decisões de evolução e impacto baseadas em uma visão comum, rastreável e atualizável. |
| **Usuários de negócio** | Compreender sistemas, processos e documentação relevante sem dominar todos os detalhes técnicos. | Onboarding e consultas de contexto com linguagem acessível, sem perder a referência técnica que sustenta a explicação. |
| **Especialistas-curadores** | Corrigir, completar e validar o que foi descoberto ou gerado. | Conhecimento humano incorporado à base, com autoria e revisão preservadas para os demais usuários autorizados. |

Os públicos podem se sobrepor: uma pessoa de arquitetura pode atuar em
sustentação e também ser curadora. O papel descreve a necessidade no contexto
de uso, não um cargo obrigatório.

### Comprador empresarial

O comprador inicial é a pessoa que aprova investimento e responde por
produtividade, risco ou continuidade do conhecimento técnico — por exemplo,
uma liderança de engenharia, arquitetura, plataforma ou tecnologia. Ela pode
não usar todas as experiências diariamente, mas precisa perceber valor em:

- reduzir dependência de especialistas e o risco de conhecimento concentrado;
- acelerar onboarding e a localização de contexto para mudanças;
- tornar decisões e investigações auditáveis por evidências;
- diminuir o custo de manter uma documentação útil em ambientes legados.

**Hipótese:** o comprador empresarial financiará um recorte vertical quando
conseguir relacionar esses resultados a uma linha de base de tempo, risco ou
esforço de manutenção de conhecimento. Essa hipótese será testada junto às
aplicações reais do MVP, sem presumir uma métrica financeira antes de medi-la.

## Experiências derivadas da base

As experiências abaixo compartilham o Knowledge Engine e a base viva. A
presença nesta visão não significa que todas serão entregues ao mesmo tempo.

- **Catálogo:** inventário navegável de fontes, aplicações, sistemas,
  componentes, documentos e relações conhecidas, com indicação de origem e
  atualidade.
- **Documentação/wiki:** páginas geradas a partir do conhecimento disponível,
  editáveis por pessoas e com revisões, evidências e sinalização de conflitos.
- **Grafo:** exploração de entidades e relações para entender contexto,
  dependências e caminhos de impacto; o grafo é uma representação útil, não o
  produto inteiro.
- **Busca e chat:** consulta textual ou conversacional que retorna contexto e
  links para evidências, deixando separadas observações, sínteses e lacunas.
- **Onboarding:** percurso guiado para que uma pessoa nova entenda uma
  aplicação ou domínio e encontre documentação relevante.
- **Análise de impacto:** apoio para identificar o que pode ser afetado por
  uma alteração, com as relações e fontes que justificam a orientação.
- **Orientação de investigação:** organização do conhecimento existente para
  sugerir onde olhar e quais relações verificar. Isso orienta o trabalho, mas
  não equivale a diagnóstico automático de causa raiz.

## Resultados esperados e como medir

Os resultados seguintes são objetivos de aprendizado do produto, não
afirmações de que o MVP já os garante:

| Resultado esperado | Sinal mensurável a acompanhar |
| --- | --- |
| Encontrar contexto relevante com menos esforço. | Tempo mediano para responder perguntas representativas de sustentação, arquitetura e negócio, comparado à linha de base do time. |
| Aumentar confiança sem criar falsa certeza. | Percentual de claims e páginas amostrados com evidência e proveniência consultáveis; conflitos e lacunas identificados explicitamente. |
| Reduzir o tempo de onboarding. | Tempo e quantidade de pedidos de esclarecimento para concluir uma tarefa de entendimento previamente definida. |
| Tornar mudanças mais previsíveis. | Número de relações de impacto encontradas e verificadas em um cenário de alteração, com as fontes que sustentam cada caminho. |
| Capturar e preservar conhecimento especialista. | Quantidade de páginas revisadas, corrigidas ou enriquecidas; nenhuma revisão curada perdida silenciosamente após uma nova análise. |
| Demonstrar valor para o comprador. | Decisão de continuar o uso com base em resultados observados, usuários recorrentes e uma métrica de custo, tempo ou risco escolhida com o time. |

## MVP vertical

### Recorte

O MVP deve provar um fluxo de ponta a ponta sobre **duas a quatro aplicações
reais**, escolhidas em um ambiente empresarial com contexto legado. O recorte
inclui:

1. fontes técnicas e **documentos existentes** dessas aplicações;
2. descoberta e correlação de relações sustentadas por evidências;
3. um catálogo e um conjunto mínimo de páginas de documentação/wiki geradas;
4. páginas que especialistas possam editar, revisar e enriquecer;
5. preservação do conteúdo curado quando novas análises trouxerem mudanças,
   lacunas ou conflitos;
6. uma demonstração de uso da base em pelo menos um cenário de onboarding,
   análise de impacto ou orientação de investigação.

O MVP é uma prova vertical do núcleo e de suas primeiras experiências, não
uma tentativa de entregar o catálogo, a wiki, o grafo, a busca, o chat, o
onboarding, o impacto e a investigação como produtos independentes e
completos.

### Hipóteses e sinais do recorte

Cada parte do MVP deve produzir aprendizado verificável:

| Parte do MVP | Hipótese a validar | Sinal de validação |
| --- | --- | --- |
| Duas a quatro aplicações reais, vistas em conjunto | **H1:** um contexto limitado, porém transversal, é mais útil que documentação isolada por aplicação. | Usuários conseguem localizar a aplicação, seus vizinhos e o contexto necessário para uma tarefa representativa. |
| Documentos existentes tratados como fonte de primeira classe | **H2:** documentos já mantêm conhecimento que não aparece apenas em código ou metadados. | Documentos são encontrados, relacionados a entidades/claims e citados por usuários durante a tarefa; lacunas ficam registradas. |
| Relações com evidências e proveniência | **H3:** uma relação explicável é mais confiável e acionável que um vínculo sem origem visível. | Amostra de claims aponta para fonte e momento; especialistas conseguem confirmar, contestar ou marcar conflito. |
| Catálogo e páginas geradas/editáveis | **H4:** uma primeira versão gerada reduz o custo de começar e manter documentação. | Especialistas conseguem revisar uma amostra, medir o esforço de edição e publicar páginas úteis em vez de descartá-las. |
| Revisão e curadoria humana | **H5:** especialistas aceitarão a base como lugar de trabalho se suas contribuições forem preservadas. | Revisões, correções e enriquecimentos aparecem para os usuários autorizados; nova análise sinaliza desatualização/conflito sem apagar curadoria. |
| Demonstração de onboarding, impacto ou investigação | **H6:** a mesma base atende mais de uma necessidade e gera valor observável. | Uma pessoa conclui o cenário definido com evidências e compara seu tempo, confiança ou número de escalonamentos com a linha de base. |
| Uso por uma liderança compradora | **H7:** o benefício percebido é suficiente para justificar continuidade empresarial. | O comprador identifica uma métrica de custo, tempo ou risco melhorada e decide se o recorte merece prosseguir. |

Os sinais devem ser registrados com o contexto da aplicação, a tarefa, o
usuário e a linha de base utilizada. Um resultado positivo em uma demonstração
não deve ser generalizado para todo o portfólio sem nova validação.

## Limites e não objetivos do MVP

### Restrição atual

- O recorte inicial considera uma organização por instalação e usuários
  autorizados dentro dessa fronteira. A organização é uma unidade de
  conhecimento e colaboração; detalhes de isolamento físico pertencem à
  arquitetura.
- Essa instalação poderá operar desde o MVP em dois modos: hospedada pelo
  Manu como SaaS dedicado em uma VPS, ou self-hosted no ambiente do cliente.
  Ambos são modos iniciais do mesmo recorte de uma organização por instalação,
  não opções futuras de implantação.
- Evidências, proveniência, temporalidade, estado de revisão e autoria humana
  são necessários para interpretar conhecimento relevante.
- As fontes do MVP são selecionadas para provar o fluxo com duas a quatro
  aplicações; cobertura total do ambiente não é condição de sucesso.

### Não objetivos explícitos

Não fazem parte do MVP:

- integração com ferramentas de chamados/tickets;
- ingestão de logs, métricas e traces;
- diagnóstico automático de causa raiz;
- implementação de um **Control Plane** para provisionar, licenciar ou
  operar instalações;
- SaaS compartilhado operacional com múltiplas organizações na mesma célula.

Também não são objetivos desta fundação:

- substituir especialistas ou publicar inferências sem evidência e revisão;
- transformar o Manu em apenas um grafo, uma wiki, um chat ou uma ferramenta
  de investigação;
- entregar todas as experiências derivadas simultaneamente;
- fixar agora um provedor de cloud, de IA, um modelo físico de dados ou um
  conjunto definitivo de parsers;
- criar um roadmap detalhado, pesquisas, runbooks internos do próprio Manu ou
  uma documentação de referência separada do fluxo de validação.

As integrações operacionais e o SaaS compartilhado podem ser úteis, mas ficam
registrados como opções futuras até que uma necessidade concreta e uma nova
mudança especificada justifiquem seu escopo.

## Opções futuras, não compromissos

Depois de validar o fluxo vertical, poderão ser explorados:

- integração com tickets e ingestão de logs, métricas e traces;
- orientação operacional mais ampla e, se houver evidência suficiente,
  diagnóstico automatizado de causa raiz;
- SaaS compartilhado operacional com múltiplas organizações na mesma célula;
- um Control Plane para operar várias instalações/células;
- novas fontes, domínios e experiências sobre a base viva.

Essas possibilidades não definem o MVP, não formam um roadmap implícito e não
devem alterar o núcleo: o conhecimento sustentado por fontes, evidências e
curadoria continua sendo a base para qualquer evolução.

## Questões abertas para validação

As lacunas abaixo devem ser respondidas por uso real, não por suposição:

- Quais tarefas de sustentação, arquitetura e negócio têm a melhor linha de
  base para medir tempo, risco ou esforço?
- Qual nível de evidência e explicação faz cada público confiar em uma relação
  ou página gerada?
- Quanto trabalho de revisão um especialista considera aceitável para uma
  primeira versão útil?
- Qual combinação de fontes revela valor suficiente sem exigir ingestão do
  ambiente inteiro?
- Qual modalidade de implantação e quais políticas de conteúdo serão
  necessárias quando houver mais de uma organização ou dados mais sensíveis?
