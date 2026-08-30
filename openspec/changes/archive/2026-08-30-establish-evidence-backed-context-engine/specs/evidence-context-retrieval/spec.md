## Purpose

Define como o Manu transforma uma pergunta ou alvo técnico em um pacote pequeno, autorizado e verificável de contexto, adequado para consumo humano ou por agentes dentro de um orçamento explícito.

## ADDED Requirements

### Requirement: Solicitação de contexto com escopo e orçamento explícitos
Uma solicitação de contexto MUST identificar a `Organization`, a `Source`, o `Analysis Snapshot`, a intenção de consulta e limites positivos de conteúdo. O sistema MUST rejeitar escopo ausente, ambíguo ou não autorizado e MUST registrar os limites efetivamente aplicados.

#### Scenario: Consulta válida com orçamento de tokens
- **WHEN** um consumidor solicitar contexto para uma pergunta com snapshot e `max_tokens` válidos
- **THEN** o sistema MUST executar a recuperação somente nesse escopo e MUST limitar a representação retornada ao orçamento efetivo

#### Scenario: Snapshot omitido em fonte ambígua
- **WHEN** houver mais de um snapshot elegível e o consumidor não selecionar um nem usar um alias resolvível por política
- **THEN** o sistema MUST recusar a consulta ou solicitar seleção sem combinar silenciosamente revisões diferentes

### Requirement: Recuperação híbrida e reproduzível
O sistema MUST poder combinar correspondência exata, busca textual, similaridade semântica e relações sustentadas quando esses sinais estiverem disponíveis. A fusão, os limites e os critérios de desempate MUST ser versionados e reproduzíveis; sinais indisponíveis MUST produzir degradação explícita sem fabricar candidatos.

#### Scenario: Evidência encontrada por sinais complementares
- **WHEN** termos exatos identificarem um símbolo e relações sustentadas conectarem esse símbolo a uma configuração relevante
- **THEN** o pacote MUST poder incluir ambas as evidências e registrar os sinais que contribuíram para sua seleção

#### Scenario: Busca vetorial indisponível
- **WHEN** embeddings não estiverem configurados, autorizados ou compatíveis com a projeção ativa
- **THEN** a recuperação MUST continuar com sinais exatos, textuais e relacionais disponíveis e MUST declarar a degradação vetorial

### Requirement: Seleção útil sob orçamento
O compositor MUST priorizar relevância para a intenção, cobertura dos aspectos recuperados, diversidade de artefatos e tipos e continuidade das relações, respeitando simultaneamente limites de tokens, itens, caracteres, bytes e política. O resultado MUST registrar candidatos incluídos e excluídos com razões controladas e MUST aplicar desempate determinístico.

#### Scenario: Candidatos redundantes excedem o orçamento
- **WHEN** vários candidatos semelhantes competirem com evidências de tipos ou artefatos diferentes dentro de um orçamento limitado
- **THEN** o compositor MUST aplicar os critérios versionados de diversidade e relevância e MUST registrar quais candidatos foram excluídos por orçamento ou redundância

#### Scenario: Evidência indispensável não cabe no orçamento
- **WHEN** uma unidade necessária para sustentar uma conclusão não couber integralmente no orçamento permitido
- **THEN** o sistema MUST limitar a conclusão ou retornar continuação explícita e MUST NOT citar conteúdo truncado como se a unidade completa tivesse sido examinada

### Requirement: Pacote estruturado de contexto e evidências
O pacote retornado MUST informar identidade e revisão do pacote, escopo, intenção, fatos ou entidades relevantes, relações ou caminhos possíveis, unidades de evidência, locadores, proveniência, cobertura, lacunas, degradações, estimativa de tokens e indicação de truncamento ou continuidade. Observação, derivação, geração e curadoria MUST permanecer distinguíveis.

#### Scenario: Agente recebe direção para a fonte
- **WHEN** um pacote identificar o símbolo mais relevante para uma investigação
- **THEN** a resposta MUST incluir um locador autorizado com artefato, revisão e posição suficiente para o agente inspecionar a fonte original

#### Scenario: Contexto material ausente
- **WHEN** a análise não sustentar comportamento em runtime ou outra dimensão material para a pergunta
- **THEN** o pacote MUST incluir a lacuna correspondente e MUST NOT preencher essa ausência com conhecimento geral do consumidor ou do modelo

### Requirement: Continuação estável sem repetição indevida
Quando o contexto elegível exceder o orçamento, o sistema MUST fornecer continuação opaca vinculada ao mesmo escopo, snapshot, intenção, configuração de recuperação e ordenação. A continuação MUST NOT autorizar dados adicionais nem repetir unidades já entregues, salvo quando uma referência resumida for necessária para preservar coerência.

#### Scenario: Segunda página do contexto
- **WHEN** o consumidor solicitar continuação válida de um pacote truncado
- **THEN** o sistema MUST retornar o próximo conjunto determinístico de unidades sob o mesmo orçamento e políticas

#### Scenario: Continuação usada após mudança de snapshot
- **WHEN** um cursor de continuação for usado contra revisão, escopo ou configuração incompatível
- **THEN** o sistema MUST rejeitá-lo como obsoleto ou inválido sem redirecioná-lo silenciosamente ao estado atual

### Requirement: Recuperação independente de geração
Consulta, seleção, inspeção de evidências e navegação por locadores MUST permanecer disponíveis sem um `Generator`. Quando uma LLM for usada, ela MUST receber somente a representação autorizada do pacote e seus resultados MUST continuar qualificados como conhecimento gerado.

#### Scenario: Consumidor externo usa somente dados estruturados
- **WHEN** um agente consultar o pacote sem solicitar geração pelo Manu
- **THEN** o sistema MUST retornar fatos, relações, evidências e lacunas determinísticos sem exigir configuração de modelo

#### Scenario: Transferência externa proibida
- **WHEN** a consulta local for autorizada, mas a política proibir transferência de conteúdo a um provedor externo
- **THEN** o pacote local autorizado MUST continuar consultável e a etapa externa MUST permanecer bloqueada
