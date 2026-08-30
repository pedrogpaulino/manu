# Agent Context Interface Specification

## Purpose

Define uma interface interoperável e somente leitura para que agentes de IA consultem contexto e evidências do Manu sem receber acesso direto às fontes, à persistência ou a operações administrativas.

## Requirements

### Requirement: Servidor MCP local e independente de modelo
O Manu MUST oferecer um servidor MCP local por transporte `stdio`, compatível com clientes que implementem a versão de protocolo declarada. A interface MUST reutilizar portas de aplicação do Knowledge Engine, MUST NOT depender de um provedor ou modelo específico e MUST continuar útil sem geração por IA.

#### Scenario: Cliente MCP compatível inicia o servidor
- **WHEN** um cliente autorizado iniciar o servidor local e concluir a negociação do protocolo suportado
- **THEN** o Manu MUST anunciar suas capacidades e ferramentas de leitura com schemas versionados e ordem determinística

#### Scenario: Nenhum modelo está configurado
- **WHEN** um cliente MCP consultar fatos ou evidências em uma instalação sem `Generator`
- **THEN** as ferramentas determinísticas MUST permanecer disponíveis e MUST indicar somente capacidades realmente configuradas

### Requirement: Superfície mínima de ferramentas de contexto
O servidor MUST expor operações somente leitura equivalentes a consultar contexto por pergunta, obter contexto local de uma entidade ou símbolo, analisar impacto possível e inspecionar evidências por identidade. Cada ferramenta MUST aceitar escopo e orçamento aplicáveis e MUST retornar conteúdo estruturado validável.

#### Scenario: Consulta direciona o agente
- **WHEN** o agente perguntar onde investigar uma regra em uma aplicação
- **THEN** a operação de consulta MUST retornar um pacote limitado com entidades, relações, evidências, locadores, cobertura e lacunas relevantes

#### Scenario: Impacto permanece qualificado
- **WHEN** o agente solicitar impacto de alteração sobre um símbolo ou componente
- **THEN** a operação MUST retornar caminhos sustentados como impacto possível e MUST NOT descrevê-los como execução observada sem telemetria correspondente

#### Scenario: Evidência é inspecionada por identidade
- **WHEN** o agente solicitar uma evidência retornada anteriormente
- **THEN** a operação MUST validar novamente escopo e autorização e retornar seu locador, proveniência, revisão e conteúdo permitido ou o estado controlado de indisponibilidade

### Requirement: Isolamento e autorização antes da recuperação
Toda chamada MCP MUST resolver `Organization`, `Source` e snapshot autorizados antes de recuperar conteúdo. O servidor MUST NOT expor conexão com PostgreSQL, consultas SQL ou de grafo arbitrárias, caminhos de fontes não autorizadas, credenciais, ferramentas de mutação ou meios de contornar as políticas da aplicação.

#### Scenario: Escopo fora da organização
- **WHEN** uma ferramenta receber identidade pertencente a outra organização ou fonte não permitida
- **THEN** a chamada MUST falhar sem revelar existência, metadados, conteúdo ou locadores do recurso protegido

#### Scenario: Consumidor tenta consulta arbitrária à persistência
- **WHEN** um cliente solicitar uma operação que não faça parte da superfície de leitura declarada
- **THEN** o servidor MUST rejeitá-la antes de acessar o backend e MUST NOT oferecer fallback para SQL, Cypher ou outra linguagem de consulta livre

### Requirement: Respostas limitadas, continuáveis e auditáveis
Cada ferramenta que possa retornar contexto variável MUST aceitar orçamento limitado, informar a estimativa efetiva da resposta e usar a continuidade definida pela recuperação quando necessário. A execução MUST registrar ferramenta, escopo, snapshot, orçamento, resultado, duração e identidades entregues sem registrar credenciais ou conteúdo proibido.

#### Scenario: Resposta excederia o orçamento
- **WHEN** uma chamada MCP selecionar contexto maior que o `max_tokens` permitido
- **THEN** o servidor MUST retornar um pacote válido dentro do limite com indicação de truncamento e continuação, em vez de despejar todo o resultado

#### Scenario: Auditoria da entrega a um agente
- **WHEN** uma chamada concluir com evidências autorizadas
- **THEN** a auditoria MUST permitir identificar quais evidências e revisão foram disponibilizadas, sem persistir segredos ou payloads externos desnecessários

### Requirement: Recursos e locadores referenciáveis
O servidor MUST representar evidências e artefatos permitidos por identificadores ou recursos estáveis vinculados a `Organization`, `Source` e snapshot. Uma referência MUST ser revalidada no momento da leitura e MUST indicar quando o snapshot não é mais o ativo sem alterar silenciosamente a revisão solicitada.

#### Scenario: Recurso de evidência é lido posteriormente
- **WHEN** um cliente abrir um recurso retornado por uma ferramenta
- **THEN** o servidor MUST resolver a mesma evidência e revisão autorizadas ou informar que o recurso ficou indisponível

#### Scenario: Existe snapshot mais recente
- **WHEN** o cliente consultar uma referência válida de snapshot histórico e houver uma revisão ativa posterior
- **THEN** o servidor MUST preservar o resultado histórico e MAY indicar a existência de revisão mais recente sem substituí-la automaticamente

### Requirement: Falhas MCP controladas e sem ampliação de acesso
Entradas inválidas, limites excedidos, indisponibilidade de projeção, cursor obsoleto e falhas parciais MUST produzir erros ou degradações estruturados e limitados. Mensagens MUST NOT incluir credenciais, conteúdo negado, detalhes internos da persistência ou enumeração de recursos fora do escopo.

#### Scenario: Projeção opcional indisponível
- **WHEN** uma consulta MCP puder continuar sem busca vetorial ou outro sinal opcional
- **THEN** a ferramenta MUST retornar o contexto obtido pelos sinais restantes e a degradação controlada

#### Scenario: Entrada malformada
- **WHEN** os argumentos de uma ferramenta não corresponderem ao schema publicado
- **THEN** o servidor MUST rejeitar a chamada sem executar recuperação nem ecoar conteúdo sensível recebido
