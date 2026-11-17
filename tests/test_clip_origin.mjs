import assert from 'node:assert/strict';
import {createOriginProbe} from '../web/clip-origin.js';

const entity = 'C:\\widow\\Entities\\Gameplay3P.owentity';
const model = {id:'body', bones:[
  {name:'Head',parent:2,pos:[0,0.2,0]},
  {name:'Root',parent:-1,pos:[0,0,0]},
  {name:'Neck',parent:1,pos:[0,1.4,0]},
  {name:'Camera',parent:1,pos:[0,1.65,0]},
]};
const pos = (name, values) => ({name,positions:{frames:[0],values}});
const probe = createOriginProbe(entity, model);
assert.deepEqual(probe.names, ['Root', 'Neck', 'Head']);
assert.equal(probe.matches([pos('Head',[0,0,0])]),false, 'A local zero on a standing character stays visible');
assert.equal(probe.matches([pos('Neck',[0,0,0]),pos('Head',[0,0.04,0])]),true);
assert.equal(probe.matches([pos('Neck',[0,0,0]),pos('Head',[1,0,0])]),false);
assert.equal(probe.matches([pos('Camera',[0,0,0])]),false, 'Prefer an anatomical head over an independent camera');
assert.equal(probe.matches([{name:'Head',positions:{frames:[0],values:[0,NaN,0]}}]),false);
assert.equal(probe.matches(null),false);
assert.equal(createOriginProbe(entity,{...model, parentId:'weapon'}),null);
assert.equal(createOriginProbe('Gameplay1P.owentity',model),null);
assert.equal(createOriginProbe('Gameplay3P.owmdl',model),null);
assert.equal(createOriginProbe(entity,{bones:[{name:'Head',parent:-1,pos:[0,0,0]}]}),null);
assert.equal(createOriginProbe(entity,{bones:[{name:'Head',parent:0}]}),null);
const cameraOnly = {bones:[{name:'bone_007D',parent:-1,pos:[0,1.6,0]}]};
assert.equal(createOriginProbe(entity,cameraOnly).matches([pos('bone_007D',[0,-1.6,0])]),true);
console.log('Gameplay3P frame-zero origin filter passed');
