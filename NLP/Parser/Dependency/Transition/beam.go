package Transition

import (
	"chukuparser/Algorithm/Model/Perceptron"
	BeamSearch "chukuparser/Algorithm/Search"
	"chukuparser/Algorithm/Transition"
	"chukuparser/NLP"
	"chukuparser/NLP/Parser/Dependency"
	"container/heap"
	"log"
	"sort"
	"sync"
	"time"
)

type Beam struct {
	Base             DependencyConfiguration
	TransFunc        Transition.TransitionSystem
	FeatExtractor    Perceptron.FeatureExtractor
	Model            Dependency.ParameterModel
	Size             int
	NumRelations     int
	ReturnModelValue bool
	ReturnSequence   bool
	ReturnWeights    bool
	Log              bool
	ShortTempAgenda  bool
	currentBeamSize  int
	lastRoundStart   time.Time
	durTotal         time.Duration
	durExpanding     time.Duration
	durInserting     time.Duration
	durInsertFeat    time.Duration
	durInsertScor    time.Duration
}

var _ BeamSearch.Interface = &Beam{}
var _ Perceptron.EarlyUpdateInstanceDecoder = &Beam{}
var _ Dependency.DependencyParser = &Beam{}

func (b *Beam) StartItem(p BeamSearch.Problem) BeamSearch.Candidates {
	sent, ok := p.(NLP.TaggedSentence)
	if !ok {
		panic("Problem should be an NLP.TaggedSentence")
	}
	if b.Base == nil {
		panic("Set Base to a DependencyConfiguration to parse")
	}
	if b.TransFunc == nil {
		panic("Set Transition to a Transition.TransitionSystem to parse")
	}
	if b.Model == nil {
		panic("Set Model to Dependency.ParameterModel to parse")
	}
	if b.NumRelations == 0 {
		panic("Number of relations not set")
	}
	b.Base.Conf().Init(sent)

	firstCandidates := make([]BeamSearch.Candidate, 1)
	var modelValue Dependency.ParameterModelValue
	if b.ReturnModelValue {
		modelValue = b.Model.NewModelValue()
	}
	firstCandidates[0] = &ScoredConfiguration{b.Base, 0.0, modelValue}
	b.currentBeamSize = 0
	return firstCandidates
}

func (b *Beam) getMaxSize() int {
	return b.Base.Graph().NumberOfNodes() * 2
}

func (b *Beam) Clear() BeamSearch.Agenda {
	// beam size * # of transitions
	b.lastRoundStart = time.Now()
	b.currentBeamSize += 1
	return NewAgenda(b.estimatedTransitions())
}

func (b *Beam) Insert(cs chan BeamSearch.Candidate, a BeamSearch.Agenda) BeamSearch.Agenda {
	var (
		lastMem            time.Time
		featuring, scoring time.Duration
	)
	start := time.Now()
	tempAgenda := NewAgenda(b.estimatedTransitions())
	tempAgendaHeap := heap.Interface(tempAgenda)
	heap.Init(tempAgendaHeap)
	for c := range cs {
		currentScoredConf := c.(*ScoredConfiguration)
		conf := currentScoredConf.C
		lastMem = time.Now()
		feats := b.FeatExtractor.Features(conf)
		featuring += time.Since(lastMem)
		if b.ReturnModelValue {
			featsAsWeights := b.Model.ModelValueOnes(feats)
			currentScoredConf.ModelValue.Increment(featsAsWeights)
			currentScoredConf.Score = b.Model.WeightedValue(currentScoredConf.ModelValue).Score()
		} else {
			lastMem = time.Now()
			directScoreCur := b.Model.Model().(*Perceptron.LinearPerceptron).Weights.DotProductFeatures(feats)
			directScore := directScoreCur + currentScoredConf.Score
			scoring += time.Since(lastMem)

			currentScoredConf.Score = directScore
		}
		// if b.ShortTempAgenda && tempAgenda.Len() == b.Size {
		// 	// if the temp. agenda is the size of the beam
		// 	// there is no reason to add a new one if we can prune
		// 	// some in the beam's Insert function
		// 	if tempAgenda.confs[0].Score > currentScoredConf.Score {
		// 		// if the current score has a worse score than the
		// 		// worst one in the temporary agenda, there is no point
		// 		// to adding it
		// 		continue
		// 	} else {
		// 		heap.Pop(tempAgendaHeap)
		// 	}
		// }
		heap.Push(tempAgendaHeap, currentScoredConf)
	}
	agenda := a.(*Agenda)
	agenda.Lock()
	agenda.confs = append(agenda.confs, tempAgenda.confs...)
	agenda.Unlock()
	insertDuration := time.Since(start)
	b.durInserting += insertDuration
	b.durInsertFeat += featuring
	b.durInsertScor += scoring
	// log.Println("Time featuring (pct):\t", featuring.Nanoseconds(), 100*featuring/insertDuration)
	// log.Println("Time converting (pct):\t", converting.Nanoseconds(), 100*converting/insertDuration)
	// log.Println("Time weighing (pct):\t", weighing.Nanoseconds(), 100*weighing/insertDuration)
	// log.Println("Time scoring (pct):\t", scoring.Nanoseconds(), 100*scoring/insertDuration)
	// log.Println("Time dot scoring (pct):\t", dotScoring.Nanoseconds())
	// log.Println("Inserting Total:", insertDuration)
	// log.Println("Beam State", b.currentBeamSize, "/", b.getMaxSize(), "Ending insert")
	return agenda
}

func (b *Beam) estimatedTransitions() int {
	return b.NumRelations*2 + 2
}

func (b *Beam) Expand(c BeamSearch.Candidate, p BeamSearch.Problem) chan BeamSearch.Candidate {
	var (
		modelValue    Dependency.ParameterModelValue
		lastMem       time.Time
		transitioning time.Duration
	)
	start := time.Now()
	candidate := c.(*ScoredConfiguration)
	conf := candidate.C
	retChan := make(chan BeamSearch.Candidate, b.estimatedTransitions())
	go func(currentConf DependencyConfiguration, candidateChan chan BeamSearch.Candidate) {
		for transition := range b.TransFunc.YieldTransitions(currentConf.Conf()) {
			lastMem = time.Now()
			newConf := b.TransFunc.Transition(currentConf.Conf(), transition)
			transitioning += time.Since(lastMem)

			if b.ReturnModelValue {
				modelValue = candidate.ModelValue.Copy()
			}
			// at this point, the candidate has it's *previous* score
			// insert will do compute newConf's features and model score
			// this is done to allow for maximum concurrency
			// where candidates are created while others are being scored before
			// adding into the agenda
			candidateChan <- &ScoredConfiguration{newConf.(DependencyConfiguration), candidate.Score, modelValue}
		}
		close(candidateChan)
	}(conf, retChan)
	b.durExpanding += time.Since(start)
	return retChan
}

func (b *Beam) Top(a BeamSearch.Agenda) BeamSearch.Candidate {
	agenda := a.(*Agenda)
	agendaHeap := heap.Interface(agenda)
	agenda.HeapReverse = true
	heap.Init(agendaHeap)
	// peeking into an initialized (heapified) array
	best := agenda.confs[0]
	sort.Sort(agendaHeap)
	return best
}

func (b *Beam) GoalTest(p BeamSearch.Problem, c BeamSearch.Candidate) bool {
	conf := c.(*ScoredConfiguration).C
	return conf.Conf().Terminal()
}

func (b *Beam) TopB(a BeamSearch.Agenda, B int) BeamSearch.Candidates {
	candidates := make([]BeamSearch.Candidate, 0, B)
	agendaHeap := a.(heap.Interface)
	// assume agenda heap is already heapified
	heap.Init(agendaHeap)
	for i := 0; i < B; i++ {
		if len(a.(*Agenda).confs) > 0 {
			candidates = append(candidates, heap.Pop(agendaHeap))
		} else {
			break
		}
	}
	b.durTotal += time.Since(b.lastRoundStart)

	return candidates
}

func (b *Beam) Parse(sent NLP.Sentence, constraints Dependency.ConstraintModel, model Dependency.ParameterModel) (NLP.DependencyGraph, interface{}) {
	prefix := log.Prefix()
	log.SetPrefix("Parsing ")
	b.Model = model
	// log.Println("Starting parse")

	beamScored := BeamSearch.Search(b, sent, b.Size).(*ScoredConfiguration)

	// build result parameters
	var resultParams *ParseResultParameters
	if b.ReturnModelValue || b.ReturnSequence {
		resultParams = new(ParseResultParameters)
		if b.ReturnModelValue {
			resultParams.modelValue = beamScored.ModelValue
		}
		if b.ReturnSequence {
			resultParams.Sequence = beamScored.C.Conf().GetSequence()
		}
	}
	configurationAsGraph := beamScored.C.(NLP.DependencyGraph)

	// log.Println("Time Expanding (pct):\t", b.durExpanding.Nanoseconds(), 100*b.durExpanding/b.durTotal)
	// log.Println("Time Inserting (pct):\t", b.durInserting.Nanoseconds(), 100*b.durInserting/b.durTotal)
	// log.Println("Time Inserting-Feat (pct):\t", b.durInsertFeat.Nanoseconds(), 100*b.durInsertFeat/b.durTotal)
	// log.Println("Time Inserting-Scor (pct):\t", b.durInsertScor.Nanoseconds(), 100*b.durInsertScor/b.durTotal)
	// log.Println("Total Time:", b.durTotal.Nanoseconds())
	log.SetPrefix(prefix)
	return configurationAsGraph, resultParams
}

// Perceptron function
func (b *Beam) DecodeEarlyUpdate(goldInstance Perceptron.DecodedInstance, m Perceptron.Model) (Perceptron.DecodedInstance, *Perceptron.SparseWeightVector, *Perceptron.SparseWeightVector) {
	prefix := log.Prefix()
	log.SetPrefix("Training ")
	// log.Println("Starting decode")
	sent := goldInstance.Instance().(NLP.Sentence)
	b.Model = Dependency.ParameterModel(&PerceptronModel{m.(*Perceptron.LinearPerceptron)})

	// abstract casting >:-[
	rawGoldSequence := goldInstance.Decoded().(Transition.Configuration).GetSequence()

	// drop the first (seq are in reverse) configuration, as it is the initial one
	// which is by definition without a score or features
	rawGoldSequence = rawGoldSequence[:len(rawGoldSequence)-1]

	goldSequence := make([]interface{}, len(rawGoldSequence))
	goldModelValue := b.Model.NewModelValue()
	for i := len(rawGoldSequence) - 1; i >= 0; i-- {
		val := rawGoldSequence[i]
		goldFeat := b.FeatExtractor.Features(val)
		goldAsWeights := b.Model.ModelValueOnes(goldFeat)
		goldModelValue.Increment(goldAsWeights)
		goldSequence[len(rawGoldSequence)-i-1] = &ScoredConfiguration{val.(DependencyConfiguration), goldModelValue.Score(), goldModelValue.Copy()}
	}

	b.ReturnModelValue = true

	// log.Println("Begin search..")
	beamResult, goldResult := BeamSearch.SearchConcurrentEarlyUpdate(b, sent, b.Size, goldSequence)
	// log.Println("Search ended")

	beamScored := beamResult.(*ScoredConfiguration)
	goldScored := goldResult.(*ScoredConfiguration)

	parsedWeights := beamScored.ModelValue.(*PerceptronModelValue).vector
	goldWeights := goldScored.ModelValue.(*PerceptronModelValue).vector

	if b.Log {
		// log.Println("Beam Sequence")
		// log.Println("\n", beamScored.C.Conf().GetSequence().String())
		// log.Println("\n", parsedWeights)
		// log.Println("Gold")
		// log.Println("\n", goldScored.C.Conf().GetSequence().String())
		// log.Println("\n", goldWeights)
	}

	parsedGraph := beamScored.C.Graph()

	// if b.Log {
	// 	log.Println("Beam Weights")
	// 	log.Println(parsedWeights)
	// 	log.Println("Gold Weights")
	// 	log.Println(goldWeights)
	// }

	log.SetPrefix(prefix)
	return &Perceptron.Decoded{goldInstance.Instance(), parsedGraph}, parsedWeights, goldWeights
}

type ScoredConfiguration struct {
	C          DependencyConfiguration
	Score      float64
	ModelValue Dependency.ParameterModelValue
}

type Agenda struct {
	sync.Mutex
	HeapReverse bool
	confs       []*ScoredConfiguration
}

func (a *Agenda) Len() int {
	return len(a.confs)
}

func (a *Agenda) Less(i, j int) bool {
	scoredI := a.confs[i]
	scoredJ := a.confs[j]
	// less in reverse, we want the highest scoring to be first in the heap
	if a.HeapReverse {
		return scoredI.Score > scoredJ.Score
	}
	return scoredI.Score < scoredJ.Score
}

func (a *Agenda) Swap(i, j int) {
	a.confs[i], a.confs[j] = a.confs[j], a.confs[i]
}

func (a *Agenda) Push(x interface{}) {
	scored := x.(*ScoredConfiguration)
	a.confs = append(a.confs, scored)
}

func (a *Agenda) Pop() interface{} {
	n := len(a.confs)
	scored := a.confs[n-1]
	a.confs = a.confs[0 : n-1]
	return scored
}

func (a *Agenda) Contains(goldCandidate BeamSearch.Candidate) bool {
	for _, candidate := range a.confs {
		if candidate.C.Equal(goldCandidate.(*ScoredConfiguration).C) {
			return true
		}
	}
	return false
}

func (a *Agenda) Candidates() BeamSearch.Candidates {
	candidates := make([]BeamSearch.Candidate, len(a.confs))
	for i, val := range a.confs {
		candidates[i] = BeamSearch.Candidate(val)
	}
	return candidates
}

var _ BeamSearch.Agenda = &Agenda{}
var _ heap.Interface = &Agenda{}

func NewAgenda(size int) *Agenda {
	newAgenda := new(Agenda)
	newAgenda.confs = make([]*ScoredConfiguration, 0, size)
	return newAgenda
}
