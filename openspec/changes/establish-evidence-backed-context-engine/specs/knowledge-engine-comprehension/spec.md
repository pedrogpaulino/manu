## MODIFIED Requirements

### Requirement: Contrato universal de compreensão
O `Knowledge Engine` MUST representar resultados de analisadores especializados por um contrato conceitual comum que permita expressar, quando sustentados pela fonte, inventário e estrutura, relações, fluxos, decisões, variações configuráveis, capacidades, erros, evolução, documentação, evidências e lacunas. O contrato MUST aceitar contribuições parciais sem exigir que toda fonte forneça todas as dimensões, MUST preservar detalhes específicos que não possam ser normalizados sem perda e MUST permitir reutilização estruturada e verificável por experiências internas e consumidores externos autorizados.

#### Scenario: Correlação entre fontes diferentes
- **WHEN** analisadores de tipos de fonte diferentes identificarem elementos relacionados do mesmo ambiente
- **THEN** o `Knowledge Engine` MUST conseguir correlacionar seus resultados por conceitos comuns, preservando a fonte, o método e o suporte de cada contribuição

#### Scenario: Dimensão não aplicável ou não suportada
- **WHEN** um analisador não puder produzir uma das dimensões do contrato para determinada fonte
- **THEN** o resultado MUST registrar a ausência de cobertura ou suporte sem fabricar conhecimento para preencher a dimensão

#### Scenario: Consumidor externo reutiliza a compreensão
- **WHEN** um consumidor autorizado solicitar contexto estruturado sobre uma entidade, relação ou fluxo compreendido
- **THEN** o `Knowledge Engine` MUST fornecer a contribuição comum com evidências, proveniência, qualificadores, cobertura e lacunas aplicáveis sem exigir que o consumidor interprete o formato privado do analisador de origem

#### Scenario: Normalização perderia semântica específica
- **WHEN** duas linguagens ou plataformas expressarem conceitos parecidos com qualificadores semanticamente diferentes
- **THEN** o contrato MUST preservar o predicado comum sustentado e os qualificadores específicos necessários, sem declarar equivalência mais forte que as fontes permitem
