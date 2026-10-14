// Synthetic evidence records only. These identifiers are not game evidence.
import assert from 'node:assert/strict';
import {filterAnalysisClips} from '../web/animation-analysis.js';
import {visibleClips} from '../web/library-clips.js';

const clips = [
  {path:'C:/Fixture/ModelA/123.owanimclip', duplicateKey:'same-bytes', annotation:{name:'My label'}},
  {path:'C:/Fixture/NamedContext/123.owanimclip', duplicateKey:'same-bytes', annotation:{name:''}},
  {path:'C:/Fixture/OtherIdentity/123.owanimclip', duplicateKey:'different-bytes', annotation:{name:''}},
  {path:'C:/Fixture/New/456.owanimclip', duplicateKey:'new-bytes', annotation:{name:''}},
];
const index = {entries:[
  {path:clips[0].path,assetId:'scoped-a',modelIds:['a'],contextIds:[],statuses:['unknown']},
  {path:clips[1].path,assetId:'scoped-a',modelIds:['b'],contextIds:['named-a','named-b'],statuses:['referenced-by-named-context']},
  {path:clips[2].path,assetId:'scoped-b',modelIds:['a','b'],contextIds:[],statuses:['suggested']},
]};
const original = JSON.stringify({clips,index});
assert.equal(filterAnalysisClips(clips,index),clips,'No filters preserves the original collection.');
assert.deepEqual(filterAnalysisClips(clips,index,{model:'a'}),[clips[0],clips[2]]);
assert.deepEqual(filterAnalysisClips(clips,index,{model:'b'}),[clips[1],clips[2]],'Compatible with multiple rigs stays ambiguous.');
assert.deepEqual(filterAnalysisClips(clips,index,{context:'named-a'}),[clips[1]]);
assert.deepEqual(filterAnalysisClips(clips,index,{context:'named-b'}),[clips[1]],'A reused clip retains both contexts.');
assert.deepEqual(filterAnalysisClips(clips,index,{status:'suggested'}),[clips[2]]);
assert.deepEqual(filterAnalysisClips(clips,index,{status:'unknown'}),[clips[0],clips[3]],'A newly scanned file has no evidence yet.');
assert.deepEqual(filterAnalysisClips(clips,index,{model:'a',context:'named-a'}),[],'Filters refer to the same occurrence.');
assert.deepEqual(filterAnalysisClips(clips,index,{model:'b',status:'suggested'}),[clips[2]]);
assert.deepEqual(visibleClips(filterAnalysisClips(clips,index,{context:'named-a'}),{selectedPath:clips[0].path}),[clips[1]],'Filter before dedupe: a selected alias cannot suppress a context-bearing occurrence.');
assert.deepEqual(visibleClips(filterAnalysisClips(clips,index,{model:'a'})),[clips[0],clips[2]],'Identical display filenames do not merge different identities.');
assert.deepEqual(filterAnalysisClips([{...clips[0],path:'c:\\fixture\\modela\\123.owanimclip'}],index,{model:'a'}).length,1,'Windows slash and case normalization does not parse identifiers as numbers.');
assert.equal(JSON.stringify({clips,index}),original,'Filtering changes no source metadata or nickname.');
console.log('13 animation evidence filter checks passed (synthetic records).');
