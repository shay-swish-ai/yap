package Transition

import (
	"chukuparser/Algorithm/Model/Perceptron"
	"chukuparser/Algorithm/Transition"
	"chukuparser/NLP"
	"chukuparser/NLP/Parser/Dependency"
	"chukuparser/Util"
	"fmt"
	"sort"
)

type Deterministic struct {
	TransFunc          Transition.TransitionSystem
	FeatExtractor      Perceptron.FeatureExtractor
	ReturnModelValue   bool
	ReturnSequence     bool
	ShowConsiderations bool
}

var _ Dependency.DependencyParser = &Deterministic{}
var _ Perceptron.InstanceDecoder = &Deterministic{}

type ParseResultParameters struct {
	modelValue interface{}
	sequence   Transition.ConfigurationSequence
}

// Parser functions
func (d *Deterministic) Parse(sent NLP.Sentence, constraints Dependency.ConstraintModel, model Dependency.ParameterModel) (NLP.DependencyGraph, interface{}) {
	if constraints != nil {
		panic("Got non-nil constraints; deterministic dependency parsing does not consider constraints")
	}
	if d.TransFunc == nil {
		panic("Can't parse without a transition system")
	}
	transitionClassifier := &TransitionClassifier{Model: model, TransFunc: d.TransFunc, FeatExtractor: d.FeatExtractor}
	transitionClassifier.Init()
	transitionClassifier.ShowConsiderations = d.ShowConsiderations
	c := Transition.Configuration(new(SimpleConfiguration))

	// deterministic parsing algorithm
	c.Init(sent)
	for !c.Terminal() {
		c, _ = transitionClassifier.TransitionWithConf(c)
		transitionClassifier.Increment(c)
		if c == nil {
			fmt.Println("Got nil configuration!")
		}
	}

	// build result parameters
	var resultParams *ParseResultParameters
	if d.ReturnModelValue || d.ReturnSequence {
		resultParams = new(ParseResultParameters)
		if d.ReturnModelValue {
			resultParams.modelValue = transitionClassifier.ModelValue
		}
		if d.ReturnSequence {
			resultParams.sequence = c.GetSequence()
		}
	}
	configurationAsGraph := c.(NLP.DependencyGraph)
	return configurationAsGraph, resultParams
}

func (d *Deterministic) ParseOracle(sent NLP.Sentence, gold NLP.DependencyGraph, constraints interface{}, model interface{}) (NLP.DependencyGraph, interface{}) {
	if constraints != nil {
		panic("Got non-nil constraints; deterministic dependency parsing does not consider constraints")
	}
	if d.TransFunc == nil {
		panic("Can't parse without a transition system")
	}
	c := Transition.Configuration(new(SimpleConfiguration))
	classifier := TransitionClassifier{Model: model.(Dependency.ParameterModel), FeatExtractor: d.FeatExtractor, TransFunc: d.TransFunc}
	c.Init(sent)
	classifier.Init()
	oracle := d.TransFunc.Oracle()
	oracle.SetGold(gold)
	for !c.Terminal() {
		transition := oracle.Transition(c)
		c = d.TransFunc.Transition(c, transition)
		classifier.Increment(c)
	}

	// build result parameters
	var resultParams *ParseResultParameters
	if d.ReturnModelValue || d.ReturnSequence {
		resultParams = new(ParseResultParameters)
		if d.ReturnModelValue {
			resultParams.modelValue = classifier.ModelValue
		}
		if d.ReturnSequence {
			resultParams.sequence = c.GetSequence()
		}
	}
	configurationAsGraph := c.(*SimpleConfiguration).Graph()
	return configurationAsGraph, resultParams
}

func (d *Deterministic) ParseOracleEarlyUpdate(sent NLP.Sentence, gold NLP.DependencyGraph, constraints interface{}, model interface{}) (NLP.DependencyGraph, interface{}, interface{}) {
	if constraints != nil {
		panic("Got non-nil constraints; deterministic dependency parsing does not consider constraints")
	}
	if d.TransFunc == nil {
		panic("Can't parse without a transition system")
	}

	// Initializations
	c := Transition.Configuration(new(SimpleConfiguration))
	classifier := TransitionClassifier{Model: model.(Dependency.ParameterModel), FeatExtractor: d.FeatExtractor, TransFunc: d.TransFunc}
	classifier.ShowConsiderations = d.ShowConsiderations
	c.Init(sent)
	classifier.Init()
	oracle := d.TransFunc.Oracle()
	oracle.SetGold(gold)

	var goldWeights interface{}
	var i int = 0
	var predTrans Transition.Transition
	for !c.Terminal() {
		goldTrans := oracle.Transition(c)
		goldConf := d.TransFunc.Transition(c, goldTrans)
		c, predTrans = classifier.TransitionWithConf(c)
		if c == nil {
			fmt.Println("Got nil configuration!")
		}

		// verify the right transition was chosen
		if predTrans != goldTrans {
			goldFeatures := d.FeatExtractor.Features(goldConf)
			goldConfWeights := classifier.Model.ModelValue(goldFeatures)
			goldWeights = classifier.ModelValue.(*PerceptronModelValue).WeightsWith(goldConfWeights)
			classifier.Increment(c)
			break
		}
		classifier.Increment(c)
		i++
	}

	// build result parameters
	var resultParams *ParseResultParameters
	if d.ReturnModelValue || d.ReturnSequence {
		resultParams = new(ParseResultParameters)
		if d.ReturnModelValue {
			resultParams.modelValue = classifier.ModelValue
		}
		if d.ReturnSequence {
			resultParams.sequence = c.GetSequence()
		}
	}
	configurationAsGraph := c.(*SimpleConfiguration).Graph()
	return configurationAsGraph, resultParams, goldWeights
}

// Perceptron functions
func (d *Deterministic) Decode(instance Perceptron.Instance, m Perceptron.Model) (Perceptron.DecodedInstance, *Perceptron.SparseWeightVector) {
	sent := instance.(NLP.Sentence)
	model := Dependency.ParameterModel(&PerceptronModel{m.(*Perceptron.LinearPerceptron)})
	d.ReturnModelValue = true
	graph, parseParamsInterface := d.Parse(sent, nil, model)
	parseParams := parseParamsInterface.(*ParseResultParameters)
	weights := parseParams.modelValue.(*PerceptronModelValue).vector
	return &Perceptron.Decoded{instance, graph}, weights
}

func (d *Deterministic) DecodeGold(goldInstance Perceptron.DecodedInstance, m Perceptron.Model) (Perceptron.DecodedInstance, *Perceptron.SparseWeightVector) {
	sent := goldInstance.Instance().(NLP.Sentence)
	model := Dependency.ParameterModel(&PerceptronModel{m.(*Perceptron.LinearPerceptron)})
	graph := goldInstance.Decoded().(NLP.DependencyGraph)
	d.ReturnModelValue = true
	parsedGraph, parseParamsInterface := d.ParseOracle(sent, graph, nil, model)
	if !graph.Equal(parsedGraph) {
		panic("Oracle parse result does not equal gold")
	}
	parseParams := parseParamsInterface.(*ParseResultParameters)
	weights := parseParams.modelValue.(*PerceptronModelValue).vector
	return &Perceptron.Decoded{goldInstance, graph}, weights
}

func (d *Deterministic) DecodeEarlyUpdate(goldInstance Perceptron.DecodedInstance, m Perceptron.Model) (Perceptron.DecodedInstance, *Perceptron.SparseWeightVector, *Perceptron.SparseWeightVector) {
	sent := goldInstance.Instance().(NLP.Sentence)
	model := Dependency.ParameterModel(&PerceptronModel{m.(*Perceptron.LinearPerceptron)})
	graph := goldInstance.Decoded().(NLP.DependencyGraph)
	d.ReturnModelValue = true
	parsedGraph, parseParamsInterface, goldWeights := d.ParseOracleEarlyUpdate(sent, graph, nil, model)
	if parsedGraph.NumberOfEdges() == graph.NumberOfEdges() && !graph.Equal(parsedGraph) {
		panic("Oracle parse result does not equal gold")
	}
	parseParams := parseParamsInterface.(*ParseResultParameters)
	weights := parseParams.modelValue.(*PerceptronModelValue).vector
	return &Perceptron.Decoded{goldInstance.Instance(), parsedGraph}, weights, goldWeights.(*Perceptron.SparseWeightVector)
}

type TransitionClassifier struct {
	Model              Dependency.ParameterModel
	TransFunc          Transition.TransitionSystem
	FeatExtractor      Perceptron.FeatureExtractor
	ModelValue         Dependency.ParameterModelValue
	ShowConsiderations bool
}

func (tc *TransitionClassifier) Init() {
	tc.ModelValue = tc.Model.NewModelValue()
}

func (tc *TransitionClassifier) DecrementModel(c Transition.Configuration) *TransitionClassifier {
	features := tc.FeatExtractor.Features(Perceptron.Instance(c))
	weights := tc.Model.ModelValue(features).(*Perceptron.SparseWeightVector)
	tc.Model.(*PerceptronModel).PerceptronModel.Weights.UpdateSubtract(weights)
	return tc
}

func (tc *TransitionClassifier) IncrementModel(c Transition.Configuration) *TransitionClassifier {
	features := tc.FeatExtractor.Features(Perceptron.Instance(c))
	weights := tc.Model.ModelValue(features).(*Perceptron.SparseWeightVector)
	tc.Model.(*PerceptronModel).PerceptronModel.Weights.UpdateAdd(weights)
	return tc
}

func (tc *TransitionClassifier) Increment(c Transition.Configuration) *TransitionClassifier {
	features := tc.FeatExtractor.Features(Perceptron.Instance(c))
	weights := tc.Model.ModelValue(features)
	tc.ModelValue.Increment(weights)
	return tc
}

func (tc *TransitionClassifier) ScoreWithConf(c Transition.Configuration) float64 {
	features := tc.FeatExtractor.Features(Perceptron.Instance(c))
	weights := tc.Model.ModelValue(features)
	return tc.ModelValue.ScoreWith(tc.Model.Model(), weights)
}

func (tc *TransitionClassifier) Transition(c Transition.Configuration) Transition.Transition {
	_, transition := tc.TransitionWithConf(c)
	return transition
}

func (tc *TransitionClassifier) TransitionWithConfCompareGold(c Transition.Configuration) (Transition.Configuration, Transition.Transition) {
	var (
		bestScore             float64
		bestConf, currentConf Transition.Configuration
		bestTransition        Transition.Transition
	)
	tChan := tc.TransFunc.YieldTransitions(c)
	for transition := range tChan {
		currentConf = tc.TransFunc.Transition(c, transition)
		currentScore := tc.ScoreWithConf(currentConf)
		if tc.ShowConsiderations {
			fmt.Println("\t\tConsidering transition", transition, "\t", currentScore)
		}
		if bestConf == nil || currentScore > bestScore {
			bestScore, bestConf, bestTransition = currentScore, currentConf, transition
		}
	}
	if bestConf == nil {
		panic("Got no best transition - what's going on here?")
	}
	if tc.ShowConsiderations {
		fmt.Println("\tChose transition", bestTransition)
	}
	return bestConf, bestTransition
}

func (tc *TransitionClassifier) TransitionWithConf(c Transition.Configuration) (Transition.Configuration, Transition.Transition) {
	var (
		bestScore             float64
		bestConf, currentConf Transition.Configuration
		bestTransition        Transition.Transition
	)
	tChan := tc.TransFunc.YieldTransitions(c)
	for transition := range tChan {
		currentConf = tc.TransFunc.Transition(c, transition)
		currentScore := tc.ScoreWithConf(currentConf)
		if tc.ShowConsiderations {
			fmt.Println("\t\tConsidering transition", transition, "\t", currentScore)
		}
		if bestConf == nil || currentScore > bestScore {
			bestScore, bestConf, bestTransition = currentScore, currentConf, transition
		}
	}
	if bestConf == nil {
		panic("Got no best transition - what's going on here?")
	}
	if tc.ShowConsiderations {
		fmt.Println("\tChose transition", bestTransition)
	}
	return bestConf, bestTransition
}

type PerceptronModel struct {
	PerceptronModel *Perceptron.LinearPerceptron
}

var _ Dependency.ParameterModel = &PerceptronModel{}

func (p *PerceptronModel) NewModelValue() Dependency.ParameterModelValue {
	newVector := make(Perceptron.SparseWeightVector)
	return Dependency.ParameterModelValue(&PerceptronModelValue{&newVector})
}

func (p *PerceptronModel) ModelValue(val interface{}) interface{} {
	features := val.([]Perceptron.Feature)
	return Perceptron.NewVectorOfOnesFromFeatures(features)
}

func (p *PerceptronModel) Model() interface{} {
	return p.PerceptronModel
}

type PerceptronModelValue struct {
	vector *Perceptron.SparseWeightVector
}

var _ Dependency.ParameterModelValue = &PerceptronModelValue{}

func (pmv *PerceptronModelValue) Score(m interface{}) float64 {
	model := m.(*Perceptron.LinearPerceptron)
	return model.Weights.DotProduct(pmv.vector)
}

func (pmv *PerceptronModelValue) WeightsWith(other interface{}) *Perceptron.SparseWeightVector {
	otherVec := other.(*Perceptron.SparseWeightVector)
	return pmv.vector.Add(otherVec)
}

func (pmv *PerceptronModelValue) ScoreWith(m interface{}, other interface{}) float64 {
	model := m.(*Perceptron.LinearPerceptron)
	otherVec := other.(*Perceptron.SparseWeightVector)
	newVec := pmv.vector.Add(otherVec)
	return model.Weights.DotProduct(newVec)
}

func (pmv *PerceptronModelValue) Increment(other interface{}) {
	featureVec := other.(*Perceptron.SparseWeightVector)
	pmv.vector.UpdateAdd(featureVec)
}

func (pmv *PerceptronModelValue) Decrement(other interface{}) {
	featureVec := other.(*Perceptron.SparseWeightVector)
	pmv.vector.UpdateSubtract(featureVec)
}

func ArrayDiff(left []Perceptron.Feature, right []Perceptron.Feature) ([]string, []string) {
	var (
		leftStr, rightStr   []string = make([]string, len(left)), make([]string, len(right))
		onlyLeft, onlyRight []string = make([]string, 0, len(left)), make([]string, 0, len(right))
	)
	for i, val := range left {
		leftStr[i] = string(val)
	}
	for i, val := range right {
		rightStr[i] = string(val)
	}
	sort.Strings(leftStr)
	sort.Strings(rightStr)
	i, j := 0, 0
	for i < len(leftStr) || j < len(rightStr) {
		switch {
		case i < len(leftStr) && j < len(rightStr):
			comp := Util.Strcmp(leftStr[i], rightStr[j])
			switch {
			case comp == 0:
				i++
				j++
			case comp < 0:
				onlyLeft = append(onlyLeft, leftStr[i])
				i++
			case comp > 0:
				onlyRight = append(onlyRight, rightStr[j])
				j++
			}
		case i < len(leftStr):
			onlyLeft = append(onlyLeft, leftStr[i])
			i++
		case j < len(rightStr):
			onlyRight = append(onlyRight, rightStr[j])
			j++
		}
	}
	return onlyLeft, onlyRight
}
