## Purpose

Define o substrato verificável pelo qual frontends especializados transformam fontes heterogêneas em fatos canônicos, extensíveis e reconstruíveis sem acoplar o Knowledge Engine a uma linguagem ou ferramenta específica.

## ADDED Requirements

### Requirement: Fatos canônicos tipados e rastreáveis
O `Knowledge Engine` MUST representar cada fato normalizado com identidade estável no `Analysis Snapshot`, tipo ou predicado, participantes ou valor, produtor, método, versão do produtor, qualificadores aplicáveis e ligação a uma ou mais unidades de evidência. Detalhes específicos de linguagem ou plataforma MUST poder ser preservados como extensões versionadas sem perder a projeção comum.

#### Scenario: Símbolo normalizado por frontend especializado
- **WHEN** um frontend identificar a definição de um símbolo em um artefato
- **THEN** o fato resultante MUST identificar o símbolo, sua definição, o snapshot, o frontend e um locador verificável na fonte

#### Scenario: Detalhe sem equivalente universal
- **WHEN** um frontend produzir uma propriedade específica de linguagem ou framework que não possua predicado comum
- **THEN** o resultado MUST preservar a propriedade em uma extensão identificada e MUST NOT convertê-la silenciosamente em um conceito universal diferente

### Requirement: Frontends substituíveis e capacidades declaradas
Cada frontend MUST declarar identidade, versão, método, tipos de fonte reconhecidos, capacidades, limitações e versões ou famílias para as quais oferece suporte. A seleção de um frontend MUST NOT ser apresentada como prova de cobertura das dimensões que ele não declarou ou não conseguiu produzir.

#### Scenario: Versão não suportada
- **WHEN** uma fonte pertencer a uma versão ou variante fora da capacidade declarada pelo frontend selecionado
- **THEN** a análise MUST preservar os resultados genéricos aplicáveis e registrar cobertura incompleta ou não suportada para a especialização ausente

#### Scenario: Substituição de ferramenta
- **WHEN** uma nova implementação produzir os mesmos predicados canônicos por outro parser, compilador, indexador ou formato de intercâmbio
- **THEN** os consumidores MUST continuar consultando o contrato comum e as contribuições MUST permanecer distinguíveis por produtor, versão e método

### Requirement: Composição aditiva de contribuições
Contribuições de frontends de linguagem, framework, pacote, configuração, documento ou formato de intercâmbio MUST ser combinadas de forma aditiva. Uma contribuição nova MUST NOT apagar silenciosamente fatos, evidências, conflitos, cobertura ou lacunas produzidos por outro frontend ou snapshot.

#### Scenario: Linguagem e framework analisam o mesmo artefato
- **WHEN** um frontend de linguagem identificar um endpoint e um frontend de framework acrescentar sua configuração de acesso
- **THEN** o Knowledge Engine MUST relacionar as duas contribuições e preservar a proveniência independente de ambas

#### Scenario: Frontends discordam
- **WHEN** dois frontends produzirem fatos incompatíveis para o mesmo contexto
- **THEN** os fatos MUST permanecer distinguíveis e o conflito MUST ser consultável sem escolher uma certeza única sem regra ou curadoria sustentada

### Requirement: Derivação determinística com linhagem
Todo fato derivado MUST identificar a versão da regra e os fatos de entrada que o sustentam. Para o mesmo conjunto ordenado de fatos, regras e configuração, a derivação MUST produzir resultado semanticamente equivalente e MUST poder ser reconstruída sem alterar os fatos observados de origem.

#### Scenario: Dependência transitiva derivada
- **WHEN** uma regra concluir que um componente depende transitivamente de outro a partir de relações diretas
- **THEN** a relação derivada MUST apontar para a regra aplicada e para cada relação de entrada necessária à conclusão

#### Scenario: Regra atualizada
- **WHEN** uma versão nova de uma regra for aplicada ao mesmo snapshot
- **THEN** a projeção derivada MUST poder ser reconstruída e comparada com a anterior sem reclassificar fatos observados como se tivessem sido produzidos pela nova regra

### Requirement: Atualização incremental semanticamente equivalente
O pipeline MUST poder reutilizar resultados válidos de artefatos inalterados e reprocessar artefatos alterados e o fanout afetado. O resultado incremental concluído MUST ser semanticamente equivalente ao resultado de uma análise completa com os mesmos frontends, regras, snapshot e configuração.

#### Scenario: Repetição sem alteração
- **WHEN** uma fonte for analisada novamente sem mudança de conteúdo nem de configuração efetiva dos frontends e regras
- **THEN** o Knowledge Engine MUST reutilizar ou reconhecer os resultados existentes sem duplicar fatos semanticamente idênticos

#### Scenario: Alteração localizada
- **WHEN** um símbolo mudar em um único artefato
- **THEN** o pipeline MUST invalidar os fatos desse artefato e as derivações dependentes, preservando os fatos não afetados e registrando o fanout reavaliado

### Requirement: Importação controlada e execução segura de frontends
O substrato MUST aceitar contribuições produzidas no processo local ou importadas por um protocolo versionado, aplicando as mesmas validações de escopo, identidade, proveniência e limites. O perfil determinístico padrão MUST NOT executar código da fonte, instalar dependências da fonte nem acessar a rede para produzir fatos.

#### Scenario: Índice semântico importado
- **WHEN** um índice autorizado e suportado for fornecido por uma ferramenta externa
- **THEN** o Knowledge Engine MUST validar formato, versão, escopo e locadores antes de aceitar suas contribuições e MUST registrar a ferramenta como produtora

#### Scenario: Especialização exige compilação
- **WHEN** uma especialização depender de compilador, resolução de build ou outra execução fora do perfil determinístico padrão
- **THEN** a dimensão MUST permanecer indisponível ou ser executada somente em perfil isolado explicitamente autorizado, sem enfraquecer os resultados seguros já produzidos

### Requirement: Corte heterogêneo de conformidade
O primeiro corte MUST demonstrar o mesmo substrato factual sobre Java/Quarkus, WSO2 e Python/Frappe, com profundidade declarada por frontend. O corpus MUST verificar ao menos artefatos, símbolos ou elementos nomeados, definições ou locadores, referências ou relações, dependências ou configuração e evidências quando aplicáveis a cada família.

#### Scenario: Execução nas três famílias
- **WHEN** o corpus de conformidade for processado com a configuração de referência
- **THEN** as três famílias MUST produzir fatos pelo contrato comum e MUST declarar separadamente os predicados produzidos, incompletos, não suportados, não aplicáveis ou com falha
