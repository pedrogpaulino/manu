## Purpose

Define avaliações reproduzíveis que meçam se o contexto fornecido pelo Manu reduz exploração e consumo de modelo sem sacrificar correção, evidência, segurança ou conclusão da tarefa.

## ADDED Requirements

### Requirement: Comparação controlada por tarefa
Cada avaliação de eficiência MUST usar tarefas versionadas, corpus e revisões fixados, resposta ou resultado de referência revisável, política equivalente e configuração registrada do agente, modelo e ferramentas. A linha de base MUST permitir acesso direto autorizado à fonte sem usar o contexto derivado do Manu; comparadores adicionais MUST permanecer identificados separadamente.

#### Scenario: Mesma tarefa com e sem Manu
- **WHEN** uma tarefa de localização, explicação, impacto ou alteração for avaliada
- **THEN** as execuções comparadas MUST usar a mesma revisão, objetivo e critério de sucesso e MUST registrar diferenças inevitáveis de configuração

#### Scenario: Comparador externo opcional
- **WHEN** uma ferramenta de contexto de terceiros participar da avaliação
- **THEN** sua versão, configuração, capacidades e limitações MUST ser registradas sem tratar o resultado como linha de base universal

### Requirement: Métricas de custo, exploração e qualidade
O relatório MUST registrar, quando observáveis, tokens de entrada e saída, chamadas ao modelo e a ferramentas, arquivos e bytes lidos, duração, custo externo estimado, conclusão da tarefa, correção, `evidence_recall_at_k`, `evidence_precision_at_k`, validade das citações, lacunas reconhecidas e abstinência apropriada. Métricas indisponíveis MUST ser marcadas como indisponíveis e MUST NOT receber valor zero por conveniência.

#### Scenario: Execução bem-sucedida com menos tokens
- **WHEN** a variante com Manu concluir corretamente a tarefa usando menos tokens de entrada que a linha de base
- **THEN** o relatório MUST apresentar a redução junto da correção, das evidências e das demais condições da comparação

#### Scenario: Poucos tokens com resposta incorreta
- **WHEN** uma variante economizar tokens mas falhar no resultado de referência ou nas evidências exigidas
- **THEN** o relatório MUST NOT classificar a execução como ganho de eficiência bem-sucedido

### Requirement: Eficiência condicionada a resultado correto
O indicador principal MUST comparar custo ou esforço por tarefa corretamente concluída e sustentada. Economia de tokens, latência ou leituras MUST permanecer indicadores secundários quando a execução não satisfizer os critérios de correção, autorização, evidência e abstinência.

#### Scenario: Cálculo de custo por sucesso
- **WHEN** existirem execuções válidas suficientes das variantes comparadas
- **THEN** o relatório MUST calcular ou apresentar o custo e o esforço agregados por conclusão correta, com a fórmula e o denominador utilizados

#### Scenario: Nenhuma conclusão correta
- **WHEN** uma variante não concluir corretamente nenhuma tarefa do conjunto
- **THEN** o custo por sucesso MUST ser apresentado como indefinido, e não como zero ou economia integral

### Requirement: Repetibilidade e artefatos auditáveis
Uma execução MUST registrar identidade do corpus, snapshots, casos, configuração de recuperação, orçamento, versão dos frontends e regras, versão do servidor de contexto, ferramentas do agente, modelo efetivo e digests dos resultados. Dados brutos permitidos e resumos MUST possibilitar nova execução e comparação de regressão.

#### Scenario: Reexecução após evolução do frontend
- **WHEN** um frontend, regra ou estratégia de recuperação mudar
- **THEN** o mesmo conjunto aplicável MUST poder ser repetido e comparado com a linha anterior, identificando a configuração alterada

#### Scenario: Resultado não reproduzível
- **WHEN** uma dependência móvel, revisão ausente ou configuração não registrada impedir reprodução adequada
- **THEN** o relatório MUST declarar a limitação e MUST NOT generalizar o resultado como evidência de ganho do produto

### Requirement: Alegações de economia exigem evidência delimitada
O Manu MUST NOT apresentar percentual de economia de tokens ou superioridade geral como capacidade garantida sem benchmark válido para o corpus, tarefas e configuração citados. Resultados positivos MUST ser comunicados como observados naquele contexto, com período, amostra, dispersão e limitações.

#### Scenario: Resultado positivo em um corpus
- **WHEN** a avaliação demonstrar redução estatisticamente descritível em um conjunto específico
- **THEN** qualquer comunicação baseada nela MUST identificar o conjunto, a linha de base, a métrica e as limitações e MUST NOT extrapolar automaticamente para outras linguagens ou organizações

#### Scenario: Qualidade de recuperação insuficiente
- **WHEN** recall, precisão, citações ou conclusão correta ficarem abaixo dos critérios definidos
- **THEN** a execução MUST ser tratada como falha ou aprendizado, ainda que tenha enviado menos conteúdo ao modelo
